package lanxin

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// ---- WebSocket 事件模型（官方文档 6A/6B 节）----

// Event WebSocket 推送的原始事件：type + data（data 按 type 解析）。
type Event struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type eventEnvelope struct {
	Events []Event `json:"events"`
}

// 事件类型。
const (
	EventP2PMessage    = "bot_message"           // 私聊（仅创建者）
	EventGroupMessage  = "bot_group_message"     // 群聊（全部消息，自行判断 isAtMe）
	EventApproveCardCB = "approve_card_callback" // 审批卡片按钮回调
)

// ReminderInfo 入站 reminder；box 等消息可能为 null。
type ReminderInfo struct {
	IsAtMe  bool `json:"isAtMe"`
	IsAtAll bool `json:"isAtAll"`
	Bots    []struct {
		BotID   string `json:"botId"`
		BotName string `json:"botName"`
	} `json:"bots"`
	Staffs []struct {
		StaffID   string `json:"staffId"`
		StaffName string `json:"staffName"`
	} `json:"staffs"`
}

// InboundMessage WebSocket 收到的消息（私聊/群聊共用）。
type InboundMessage struct {
	MsgType        string          `json:"msgType"`
	MsgData        json.RawMessage `json:"msgData"`
	ChatType       string          `json:"chatType"` // "p2p" | "group"
	From           string          `json:"from"`     // staffId
	SenderName     string          `json:"senderName"`
	FromType       int             `json:"fromType"` // 0=用户 1=应用
	GroupID        string          `json:"groupId"`
	GroupName      string          `json:"groupName"`
	ConversationID string          `json:"conversationId"`
	MessageID      string          `json:"messageId"` // 消息序号
	MsgID          string          `json:"msgId"`     // 消息唯一 ID（引用回复用）
	Reminder       *ReminderInfo   `json:"reminder"`
	ReferenceMsg   *InboundMessage `json:"referenceMsg"`
}

// TextContent 提取文本内容：text -> text.content，format(富文本) -> format.text。
// 其他 msgType 返回空串（媒体/卡片等由调用方按需解析 MsgData）。
func (m *InboundMessage) TextContent() string {
	switch m.MsgType {
	case "text":
		var d struct {
			Text struct {
				Content string `json:"content"`
			} `json:"text"`
		}
		if json.Unmarshal(m.MsgData, &d) == nil {
			return d.Text.Content
		}
	case "format", "formatText":
		var d struct {
			Format struct {
				Text string `json:"text"`
			} `json:"format"`
			FormatText struct {
				Text string `json:"text"`
			} `json:"formatText"`
		}
		if json.Unmarshal(m.MsgData, &d) == nil {
			if d.Format.Text != "" {
				return d.Format.Text
			}
			return d.FormatText.Text
		}
	}
	return ""
}

// mediaPart 是 msgData 里按 msgType 分段的媒体字段。
type mediaPart struct {
	MediaType MediaType `json:"mediaType"`
	MediaIDs  []string  `json:"mediaIds"`
}

// MediaAttachment 解析消息里的媒体附件。
// 图片/文件/语音/视频分别把 mediaIds 放在 image/file/voice/video 段，
// text 消息则放在 text 段并用 mediaType 标明类型。
// 返回媒体类型与 mediaId 列表；非媒体消息返回 nil。
func (m *InboundMessage) MediaAttachment() (MediaType, []string) {
	if len(m.MsgData) == 0 {
		return 0, nil
	}
	var d struct {
		Text  mediaPart `json:"text"`
		Image mediaPart `json:"image"`
		File  mediaPart `json:"file"`
		Voice mediaPart `json:"voice"`
		Video mediaPart `json:"video"`
	}
	if err := json.Unmarshal(m.MsgData, &d); err != nil {
		return 0, nil
	}

	// 先按 msgType 取对应段，避免同一条消息里其他段的重名 mediaIds 干扰。
	primary := mediaPart{}
	kind := MediaType(0)
	switch strings.ToLower(strings.TrimSpace(m.MsgType)) {
	case "image":
		primary, kind = d.Image, MediaImage
	case "file":
		primary, kind = d.File, MediaFile
	case "voice":
		primary, kind = d.Voice, MediaVoice
	case "video":
		primary, kind = d.Video, MediaVideo
	default:
		primary, kind = d.Text, d.Text.MediaType
	}
	if len(primary.MediaIDs) > 0 {
		if kind == 0 {
			kind = primary.MediaType
		}
		return kind, primary.MediaIDs
	}

	// 兼容部分网关把媒体挂在 text 段或 msgType 与段名不一致的报文。
	parts := []struct {
		kind MediaType
		part mediaPart
	}{
		{d.Text.MediaType, d.Text},
		{MediaImage, d.Image},
		{MediaFile, d.File},
		{MediaVoice, d.Voice},
		{MediaVideo, d.Video},
	}
	for _, item := range parts {
		if len(item.part.MediaIDs) == 0 {
			continue
		}
		kind := item.kind
		if kind == 0 {
			kind = item.part.MediaType
		}
		return kind, item.part.MediaIDs
	}
	return 0, nil
}

// ApproveCardCallback 审批卡片按钮回调（eventData 为发送时按钮 callbackInfo 原值）。
type ApproveCardCallback struct {
	EventData string `json:"eventData"`
	StaffID   string `json:"staffId"`
}

// ---- 客户端 ----

// EventHandler 事件回调。同步调用保证同一连接内事件顺序；阻塞会拖慢后续事件。
type EventHandler func(ctx context.Context, ev Event) error

// StatusHandler 上报 WebSocket 生命周期；message 只包含脱敏后的错误文本。
type StatusHandler func(ctx context.Context, status, message string)

// DefaultBackoff 官方建议的指数退避序列。
var DefaultBackoff = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second, 60 * time.Second}

// WS 心跳参数（官方文档 4.2 节）。
const (
	pingInterval = 30 * time.Second
	readIdle     = 70 * time.Second
)

// WSClient 维护与蓝信的 WebSocket 长连接：自动获取地址、断线重连、心跳保活。
type WSClient struct {
	client  *Client
	handler EventHandler
	status  StatusHandler
	backoff []time.Duration
}

func NewWSClient(c *Client, handler EventHandler, opts ...WSClientOption) *WSClient {
	w := &WSClient{client: c, handler: handler, backoff: DefaultBackoff}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

type WSClientOption func(*WSClient)

func WithStatusHandler(h StatusHandler) WSClientOption {
	return func(w *WSClient) { w.status = h }
}

func (w *WSClient) report(ctx context.Context, status, message string) {
	if w.status != nil {
		w.status(ctx, status, message)
	}
}

// Run 阻塞运行：直到 ctx 取消。每次重连都会重新获取 WS 地址（地址可能变化）。
func (w *WSClient) Run(ctx context.Context) error {
	i := 0
	for {
		w.report(ctx, "connecting", "")
		if err := ctx.Err(); err != nil {
			return err
		}
		ep, err := w.client.WSEndpoint(ctx)
		if err != nil {
			log.Printf("[lanxin-ws] get endpoint: %v", err)
			w.report(ctx, "error", err.Error())
		} else {
			conn, _, err := websocket.DefaultDialer.DialContext(ctx, ep, nil)
			if err == nil {
				i = 0
				log.Printf("[lanxin-ws] connected: %s", ep)
				w.report(ctx, "connected", "")
				w.serve(ctx, conn)
				conn.Close()
				log.Printf("[lanxin-ws] connection closed")
				w.report(ctx, "disconnected", "")
			} else {
				log.Printf("[lanxin-ws] dial: %v", err)
				w.report(ctx, "error", err.Error())
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		d := w.backoff[min(i, len(w.backoff)-1)]
		i++
		log.Printf("[lanxin-ws] reconnect in %s", d)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
}

func (w *WSClient) serve(ctx context.Context, conn *websocket.Conn) {
	// ctx 取消时强制断开读循环。
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	connectedAt := time.Now()
	conn.SetReadDeadline(connectedAt.Add(readIdle))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(readIdle))
	})

	// 心跳：每 30s 发 ping；70s 内无任何读（含 pong）判死。
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		t := time.NewTicker(pingInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				_ = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
			}
		}
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[lanxin-ws] read: %v", err)
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(readIdle))

		var env eventEnvelope
		if err := json.Unmarshal(raw, &env); err != nil {
			log.Printf("[lanxin-ws] bad event envelope: %v", err)
			continue
		}
		for _, ev := range env.Events {
			w.dispatch(ctx, ev)
		}
	}
}

func (w *WSClient) dispatch(ctx context.Context, ev Event) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[lanxin-ws] handler panic on event %s: %v", ev.Type, r)
		}
	}()
	if w.handler != nil {
		if err := w.handler(ctx, ev); err != nil {
			log.Printf("[lanxin-ws] handler error on event %s: %v", ev.Type, err)
		}
	}
}

// ParseEvent 便捷解析：按 type 转成 InboundMessage / ApproveCardCallback。
func ParseEvent(ev Event) (msg *InboundMessage, cb *ApproveCardCallback, err error) {
	switch ev.Type {
	case EventP2PMessage, EventGroupMessage:
		msg = &InboundMessage{}
		err = json.Unmarshal(ev.Data, msg)
		return msg, nil, err
	case EventApproveCardCB:
		cb = &ApproveCardCallback{}
		err = json.Unmarshal(ev.Data, cb)
		return nil, cb, err
	default:
		return nil, nil, nil
	}
}
