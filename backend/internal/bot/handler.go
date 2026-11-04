package bot

import (
	"context"
	"log"
	"sync"
	"time"

	"smartpipe/sdks/lanxin"
)

// Handler 消费蓝信 WebSocket 事件：归一化 -> 幂等去重 -> 会话命令 -> 分发。
type Handler struct {
	cfg  Config
	snd  Sender
	disp Dispatcher

	seenMu sync.Mutex
	seen   map[string]time.Time // msgId -> first seen，幂等键
}

func NewHandler(cfg Config, snd Sender, disp Dispatcher) *Handler {
	return &Handler{cfg: cfg, snd: snd, disp: disp, seen: map[string]time.Time{}}
}

// seenTTL 幂等窗口。蓝信正常不会重推，防的是我们自己重连期间的服务端补发。
const seenTTL = 10 * time.Minute

func (h *Handler) seenID(id string) bool {
	h.seenMu.Lock()
	defer h.seenMu.Unlock()
	now := time.Now()
	if len(h.seen) > 4096 {
		for k, ts := range h.seen {
			if now.Sub(ts) > seenTTL {
				delete(h.seen, k)
			}
		}
	}
	if _, ok := h.seen[id]; ok {
		return true
	}
	h.seen[id] = now
	return false
}

// HandleEvent 实现 lanxin.EventHandler。
func (h *Handler) HandleEvent(ctx context.Context, ev lanxin.Event) error {
	switch ev.Type {
	case lanxin.EventP2PMessage, lanxin.EventGroupMessage:
		msg, _, err := lanxin.ParseEvent(ev)
		if err != nil {
			return err
		}
		if msg == nil {
			return nil
		}
		return h.handleMessage(ctx, msg)
	case lanxin.EventApproveCardCB:
		// 审批卡片回调：M3 门禁/人工审批接入时启用。
		_, cb, err := lanxin.ParseEvent(ev)
		if err != nil {
			return err
		}
		if cb != nil {
			log.Printf("[bot] approve_card_callback: staff=%s data=%s", cb.StaffID, cb.EventData)
		}
		return nil
	default:
		return nil
	}
}

func (h *Handler) handleMessage(ctx context.Context, m *lanxin.InboundMessage) error {
	if m.MsgID == "" || h.seenID(m.MsgID) {
		return nil // 幂等：重连/补发场景
	}

	text := m.TextContent()
	// 只处理文本类；媒体消息后续随"崩溃一键上报"接入。
	if m.MsgType != "text" && m.MsgType != "format" && m.MsgType != "formatText" {
		log.Printf("[bot] skip non-text msgType=%s msgId=%s", m.MsgType, m.MsgID)
		return nil
	}

	switch m.ChatType {
	case "p2p":
		// 个人机器人只有创建者能私聊，无需白名单。
		if text == "/new" {
			return h.reply(ctx, m, "已开启新会话：此前的上下文记忆已重置。")
		}
		return h.dispatch(ctx, m, text, false)
	case "group":
		if h.cfg.RequireAtMe && (m.Reminder == nil || !m.Reminder.IsAtMe) {
			return nil // 未 @ 机器人的群消息不处理
		}
		if text == "/new" {
			return h.reply(ctx, m, "已开启新会话：此前的上下文记忆已重置。")
		}
		return h.dispatch(ctx, m, text, true)
	default:
		log.Printf("[bot] unknown chatType=%q msgId=%s", m.ChatType, m.MsgID)
		return nil
	}
}

func (h *Handler) dispatch(ctx context.Context, m *lanxin.InboundMessage, text string, isGroup bool) error {
	in := Inbound{
		Platform:          "lanxin",
		PlatformMessageID: m.MsgID,
		ChatType:          m.ChatType,
		SenderID:          m.From,
		SenderName:        m.SenderName,
		GroupID:           m.GroupID,
		ConversationID:    m.ConversationID,
		MsgType:           m.MsgType,
		Text:              text,
		IsAtMe:            m.Reminder != nil && m.Reminder.IsAtMe,
	}
	in.Reply = func(ctx context.Context, replyText string) error {
		return h.reply(ctx, m, replyText)
	}
	if err := h.disp.Dispatch(ctx, in); err != nil {
		_ = h.reply(ctx, m, "消息处理失败，请稍后重试。")
		return err
	}
	return nil
}

// reply 回复触发消息（群聊引用原消息）。
func (h *Handler) reply(ctx context.Context, m *lanxin.InboundMessage, text string) error {
	target := m.GroupID
	if m.ChatType == "p2p" {
		target = m.From
	}
	return h.SendText(ctx, m.ChatType, target, m.MsgID, text)
}

// SendText 发送文本回复：Markdown 失败回退纯文本（官方 11.9 建议）；群聊可引用原消息。
// 供消息处理与 /internal/reply 管理接口复用。
func (h *Handler) SendText(ctx context.Context, chatType, targetID, refMsgID, text string) error {
	if chatType == "p2p" {
		if _, err := h.snd.SendP2PMessage(ctx, targetID, lanxin.FormatTextMessage(text)); err == nil {
			return nil
		}
		_, err := h.snd.SendP2PMessage(ctx, targetID, lanxin.TextMessage(text))
		return err
	}
	g := lanxin.FormatTextMessage(text)
	if refMsgID != "" {
		g.Quote(refMsgID)
	}
	if _, err := h.snd.SendGroupMessage(ctx, targetID, g); err == nil {
		return nil
	}
	t := lanxin.TextMessage(text)
	if refMsgID != "" {
		t.Quote(refMsgID)
	}
	_, err := h.snd.SendGroupMessage(ctx, targetID, t)
	return err
}
