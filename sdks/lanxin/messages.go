package lanxin

import "encoding/json"

// MsgType 出站消息类型（与 API 文档 6C 节一致）。
type MsgType string

const (
	MsgTypeText        MsgType = "text"
	MsgTypeFormatText  MsgType = "formatText"
	MsgTypeLinkCard    MsgType = "linkCard"
	MsgTypeAppCard     MsgType = "appCard"
	MsgTypeApproveCard MsgType = "approveCard"
	MsgTypeAppArticles MsgType = "appArticles"
)

// MediaType 媒体类型（text 消息 mediaType 字段，1=视频 2=图片 3=文件 4=语音）。
type MediaType int

const (
	MediaVideo MediaType = 1
	MediaImage MediaType = 2
	MediaFile  MediaType = 3
	MediaVoice MediaType = 4
)

// Reminder 群聊 @ 提醒（text 消息内，群聊专用）。
type Reminder struct {
	All     bool     `json:"all,omitempty"`
	UserIDs []string `json:"userIds,omitempty"`
	BotIDs  []string `json:"botIds,omitempty"`
}

// TextContent text 消息体：纯文本，或带 mediaIds 的媒体说明。
type TextContent struct {
	Content   string    `json:"content"`
	MediaType MediaType `json:"mediaType,omitempty"`
	MediaIDs  []string  `json:"mediaIds,omitempty"`
	Reminder  *Reminder `json:"reminder,omitempty"`
}

// FormatTextContent formatText 消息体：Markdown 富文本，formatType 固定 1。
type FormatTextContent struct {
	FormatType int    `json:"formatType"`
	Text       string `json:"text"`
}

// Message 出站消息。MsgData 按官方约定以 msgType 为键。
type Message struct {
	MsgType  MsgType        `json:"msgType"`
	MsgData  map[string]any `json:"msgData"`
	RefMsgID string         `json:"refMsgId,omitempty"`
}

func newMessage(t MsgType, payload any) *Message {
	return &Message{MsgType: t, MsgData: map[string]any{string(t): payload}}
}

// RawMessage 任意 msgData（卡片等复杂结构），payload 会被 JSON 编码进 msgData[msgType]。
func RawMessage(t MsgType, payload any) *Message { return newMessage(t, payload) }

// TextMessage 纯文本消息。
func TextMessage(content string) *Message { return newMessage(MsgTypeText, TextContent{Content: content}) }

// GroupTextMessage 带 @ 提醒的群聊文本消息。
func GroupTextMessage(content string, r *Reminder) *Message {
	return newMessage(MsgTypeText, TextContent{Content: content, Reminder: r})
}

// FormatTextMessage Markdown 富文本消息（formatType 固定 1）。
func FormatTextMessage(markdown string) *Message {
	return newMessage(MsgTypeFormatText, FormatTextContent{FormatType: 1, Text: markdown})
}

// MediaTextMessage 媒体消息：先 UploadMedia 拿 mediaId 再引用。
// 视频的 mediaIDs 必须为 [视频mediaId, 封面图mediaId]。
func MediaTextMessage(content string, mt MediaType, mediaIDs ...string) *Message {
	return newMessage(MsgTypeText, TextContent{Content: content, MediaType: mt, MediaIDs: mediaIDs})
}

// Quote 回复时引用某条消息（顶层 refMsgId）。
func (m *Message) Quote(msgID string) *Message { m.RefMsgID = msgID; return m }

// ReminderAll 全员 @。
func ReminderAll() *Reminder { return &Reminder{All: true} }

// ReminderUsers 指定用户 @。
func ReminderUsers(ids ...string) *Reminder { return &Reminder{UserIDs: ids} }

// MarshalTextContent 供需要自定义 text 载荷（如同时带 reminder 与媒体）的场景使用。
func MarshalTextContent(tc TextContent) json.RawMessage {
	b, _ := json.Marshal(tc)
	return b
}
