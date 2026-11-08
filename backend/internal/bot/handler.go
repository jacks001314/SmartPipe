package bot

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"smartpipe/sdks/lanxin"
)

// Handler 消费蓝信 WebSocket 事件：归一化 -> 幂等去重 -> 会话命令 -> 分发。
type Handler struct {
	cfg   Config
	snd   Sender
	disp  Dispatcher
	crash *CrashIntake
	inbox InboxReporter

	seenMu sync.Mutex
	seen   map[string]time.Time // msgId -> first seen，幂等键
}

func NewHandler(cfg Config, snd Sender, disp Dispatcher) *Handler {
	return &Handler{cfg: cfg, snd: snd, disp: disp, seen: map[string]time.Time{}}
}

// WithCrash 挂上"一键上报"：带附件的消息走崩溃上报而不是普通派单。
func (h *Handler) WithCrash(c *CrashIntake) *Handler {
	h.crash = c
	return h
}

// WithInbox 挂上收件箱上报；未挂载时只影响记录，不影响原有处理逻辑。
func (h *Handler) WithInbox(r InboxReporter) *Handler {
	h.inbox = r
	return h
}

// reportInbox 记录一条未进入建单流程的消息；失败只记日志，不阻塞消息处理。
func (h *Handler) reportInbox(ctx context.Context, m *lanxin.InboundMessage, reason string) {
	if h.inbox == nil || m == nil || m.MsgID == "" {
		return
	}
	chatID := strings.TrimSpace(m.GroupID)
	if chatID == "" {
		chatID = m.From
	}
	err := h.inbox.Report(ctx, InboxReport{
		MessageID: m.MsgID, BotID: h.cfg.BotID, BotName: h.cfg.BotName,
		ChatType: m.ChatType, ChatID: chatID, UserID: m.From,
		SenderName: m.SenderName, GroupName: m.GroupName, MsgType: m.MsgType,
		Text: m.TextContent(), IsAtMe: m.Reminder != nil && m.Reminder.IsAtMe, Reason: reason,
	})
	if err != nil {
		log.Printf("[bot] inbox report failed msgId=%s: %v", m.MsgID, err)
	}
}

const crashHelp = "发送一个 crash_bundle.json 文件（≤256KB），我会自动建单并回执 Issue 链接。\n" +
	"字段：app、version、os、arch、exception{type,message,stack}、user_id（可选）。"

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
		// 真实网关的部分报文不带 chatType；事件类型本身就是可靠来源。
		if strings.TrimSpace(msg.ChatType) == "" {
			if ev.Type == lanxin.EventGroupMessage {
				msg.ChatType = "group"
			} else {
				msg.ChatType = "p2p"
			}
			log.Printf("[bot] chatType missing, inferred=%s type=%s msgId=%s data=%s",
				msg.ChatType, ev.Type, msg.MsgID, truncate(string(ev.Data), 2000))
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
	if mediaType, ids := m.MediaAttachment(); len(ids) > 0 {
		if m.ChatType == "group" && h.cfg.RequireAtMe && (m.Reminder == nil || !m.Reminder.IsAtMe) {
			h.reportInbox(ctx, m, "群消息未 @ 机器人")
			return nil
		}
		if h.crash == nil {
			h.reportInbox(ctx, m, "未开启一键上报")
			return h.reply(ctx, m, "当前机器人未开启一键上报，请把问题描述发给 pipeline-lead。")
		}
		chatID := m.GroupID
		if strings.TrimSpace(chatID) == "" {
			chatID = m.From
		}
		res, msg, err := h.crash.HandleAttachment(ctx, mediaType, ids[0], chatID)
		if replyErr := h.reply(ctx, m, msg); replyErr != nil {
			log.Printf("[bot] crash reply failed: %v", replyErr)
		}
		if err != nil {
			return err
		}
		if res != nil {
			log.Printf("[bot] crash ingested status=%s issue=%d msgId=%s", res.Status, res.IssueNumber, m.MsgID)
		}
		return nil
	}
	// 只处理文本类。
	if m.MsgType != "text" && m.MsgType != "format" && m.MsgType != "formatText" {
		h.reportInbox(ctx, m, "暂不处理的消息类型")
		log.Printf("[bot] skip non-text msgType=%s msgId=%s", m.MsgType, m.MsgID)
		return nil
	}
	if text == "/crash" && h.crash != nil {
		h.reportInbox(ctx, m, "命令已在本地处理")
		return h.reply(ctx, m, crashHelp)
	}

	switch m.ChatType {
	case "p2p":
		// 个人机器人只有创建者能私聊，无需白名单。
		if text == "/new" {
			h.reportInbox(ctx, m, "命令已在本地处理")
			return h.reply(ctx, m, "已开启新会话：此前的上下文记忆已重置。")
		}
		return h.dispatch(ctx, m, text, false)
	case "group":
		if h.cfg.RequireAtMe && (m.Reminder == nil || !m.Reminder.IsAtMe) {
			h.reportInbox(ctx, m, "群消息未 @ 机器人")
			return nil // 未 @ 机器人的群消息不处理
		}
		if text == "/new" {
			h.reportInbox(ctx, m, "命令已在本地处理")
			return h.reply(ctx, m, "已开启新会话：此前的上下文记忆已重置。")
		}
		return h.dispatch(ctx, m, text, true)
	default:
		h.reportInbox(ctx, m, "未知会话类型")
		log.Printf("[bot] unknown chatType=%q msgId=%s groupId=%s", m.ChatType, m.MsgID, m.GroupID)
		return nil
	}
}

func (h *Handler) dispatch(ctx context.Context, m *lanxin.InboundMessage, text string, isGroup bool) error {
	in := Inbound{
		Platform:          "lanxin",
		BotID:             h.cfg.BotID,
		BotName:           h.cfg.BotName,
		PlatformMessageID: m.MsgID,
		ChatType:          m.ChatType,
		SenderID:          m.From,
		SenderName:        m.SenderName,
		GroupID:           m.GroupID,
		GroupName:         m.GroupName,
		ConversationID:    m.ConversationID,
		MsgType:           m.MsgType,
		Text:              text,
		IsAtMe:            m.Reminder != nil && m.Reminder.IsAtMe,
	}
	in.Reply = func(ctx context.Context, replyText string) error {
		return h.reply(ctx, m, replyText)
	}
	if err := h.disp.Dispatch(ctx, in); err != nil {
		if errors.Is(err, ErrUnboundChat) {
			_ = h.reply(ctx, m, "这个群还没有绑定项目，请联系管理员在「蓝信配置」里绑定后再发送。")
		} else if errors.Is(err, ErrNoGitHubCredential) {
			_ = h.reply(ctx, m, "消息已收到，但项目还没有配置 GitHub 凭据；管理员配置后即可自动建单。")
		} else {
			_ = h.reply(ctx, m, "消息处理失败，请稍后重试。")
		}
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
