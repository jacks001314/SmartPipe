package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"smartpipe/sdks/lanxin"
)

// Handler 消费蓝信 WebSocket 事件：归一化 -> 幂等去重 -> 会话命令 -> 分发。
type Handler struct {
	cfg       Config
	snd       Sender
	disp      Dispatcher
	crash     *CrashIntake
	inbox     InboxReporter
	media     MediaDownloader
	directory Directory

	seenMu sync.Mutex
	seen   map[string]time.Time // msgId -> first seen，幂等键

	dirMu       sync.Mutex
	memberCache map[string]memberNameCache
	groupCache  map[string]groupNameCache
}

func NewHandler(cfg Config, snd Sender, disp Dispatcher) *Handler {
	return &Handler{
		cfg: cfg, snd: snd, disp: disp, seen: map[string]time.Time{},
		memberCache: map[string]memberNameCache{}, groupCache: map[string]groupNameCache{},
	}
}

// WithCrash 挂上"一键上报"：带附件的消息走崩溃上报而不是普通派单。
func (h *Handler) WithCrash(c *CrashIntake) *Handler {
	h.crash = c
	return h
}

// WithMedia 挂上媒体下载能力；图片/文件消息会下载后随消息交给模型。
func (h *Handler) WithMedia(m MediaDownloader) *Handler {
	h.media = m
	return h
}

// WithDirectory 挂上通讯录查询，消息缺少 senderName 时按 staffId 补真实姓名。
func (h *Handler) WithDirectory(d Directory) *Handler {
	h.directory = d
	return h
}

// MediaDownloader 按蓝信 mediaId 取回文件内容。
type MediaDownloader interface {
	DownloadMedia(ctx context.Context, mediaID string) ([]byte, error)
}

// Directory 用于把 staffId / groupId 补成真实姓名和群名。
type Directory interface {
	GroupInfo(ctx context.Context, groupID string) (*lanxin.GroupInfo, error)
	GroupMembers(ctx context.Context, groupID string, offset, limit int) ([]lanxin.GroupMember, error)
}

type memberNameCache struct {
	names map[string]string
	at    time.Time
}

type groupNameCache struct {
	name string
	at   time.Time
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

const directoryCacheTTL = 10 * time.Minute

// resolveMessageNames 补全蓝信事件里缺失的群名和发送人姓名。
// 真实网关偶尔只给 staffId/groupId，不补的话 Issue 正文只能显示一串 ID。
func (h *Handler) resolveMessageNames(ctx context.Context, m *lanxin.InboundMessage) {
	if h.directory == nil || m == nil || strings.TrimSpace(m.GroupID) == "" {
		return
	}
	if strings.TrimSpace(m.GroupName) == "" {
		if name := h.cachedGroupName(ctx, m.GroupID); name != "" {
			m.GroupName = name
		}
	}
	if strings.TrimSpace(m.SenderName) == "" && strings.TrimSpace(m.From) != "" {
		if name := h.cachedMemberName(ctx, m.GroupID, m.From); name != "" {
			m.SenderName = name
		}
	}
}

func (h *Handler) cachedGroupName(ctx context.Context, groupID string) string {
	h.dirMu.Lock()
	cached, ok := h.groupCache[groupID]
	h.dirMu.Unlock()
	if ok && time.Since(cached.at) < directoryCacheTTL {
		return cached.name
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	info, err := h.directory.GroupInfo(callCtx, groupID)
	name := ""
	if err != nil {
		log.Printf("[bot] resolve group name %s failed: %v", groupID, err)
	} else if info != nil {
		name = strings.TrimSpace(info.Name)
	}
	h.dirMu.Lock()
	if h.groupCache == nil {
		h.groupCache = map[string]groupNameCache{}
	}
	h.groupCache[groupID] = groupNameCache{name: name, at: time.Now()}
	h.dirMu.Unlock()
	return name
}

func (h *Handler) cachedMemberName(ctx context.Context, groupID, staffID string) string {
	h.dirMu.Lock()
	cached, ok := h.memberCache[groupID]
	h.dirMu.Unlock()
	if ok && time.Since(cached.at) < directoryCacheTTL {
		return cached.names[staffID]
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	names := map[string]string{}
	for offset := 0; offset < 1000; offset += 100 {
		page, err := h.directory.GroupMembers(callCtx, groupID, offset, 100)
		if err != nil {
			log.Printf("[bot] resolve group members %s failed: %v", groupID, err)
			break
		}
		for _, member := range page {
			if name := strings.TrimSpace(member.Name); member.StaffID != "" && name != "" {
				names[member.StaffID] = name
			}
		}
		if len(page) < 100 {
			break
		}
	}
	h.dirMu.Lock()
	if h.memberCache == nil {
		h.memberCache = map[string]memberNameCache{}
	}
	h.memberCache[groupID] = memberNameCache{names: names, at: time.Now()}
	h.dirMu.Unlock()
	return names[staffID]
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

func isExplicitBotCommand(text string) bool {
	t := strings.TrimSpace(text)
	for strings.HasPrefix(t, "@") {
		fields := strings.Fields(t)
		if len(fields) == 0 {
			return false
		}
		t = strings.TrimSpace(strings.TrimPrefix(t, fields[0]))
	}
	return strings.HasPrefix(t, "/") || strings.HasPrefix(t, "确认建单") || strings.HasPrefix(t, "取消建单") || strings.HasPrefix(t, "撤回建单") || strings.HasPrefix(t, "撤销建单")
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
	isAtMe := m.Reminder != nil && m.Reminder.IsAtMe
	replyToBot := m.ReferenceMsg != nil && m.ReferenceMsg.FromType == 1
	explicitCommand := isExplicitBotCommand(text)
	observeAll := m.ChatType == "group" && strings.TrimSpace(h.cfg.SchedulerURL) != ""
	nonDirect := m.ChatType == "group" && !isAtMe && !replyToBot && !explicitCommand
	if m.ChatType == "group" && (observeAll || !h.cfg.RequireAtMe || isAtMe || replyToBot || explicitCommand) {
		h.resolveMessageNames(ctx, m)
	}

	if mediaType, ids := m.MediaAttachment(); len(ids) > 0 {
		if nonDirect && !observeAll {
			h.reportInbox(ctx, m, "群消息未 @ 机器人")
			return nil
		}
		// @ 的 crash bundle 走一键上报；非 @ 消息交给调度器观察，避免影子模式直接建草稿。
		if h.crash != nil && isCrashBundle(m, text) && !nonDirect {
			chatID := m.GroupID
			if strings.TrimSpace(chatID) == "" {
				chatID = m.From
			}
			res, msg, err := h.crash.Handle(ctx, CrashAttachment{
				MediaType: mediaType, MediaID: ids[0], ChatID: chatID, MessageID: m.MsgID,
				BotID: h.cfg.BotID, BotName: h.cfg.BotName, GroupName: m.GroupName,
				SenderID: m.From, SenderName: m.SenderName,
			})
			// 建单不再等群里确认：崩溃草稿生成后立刻交给调度器建 Issue 并派 worker。
			if err == nil && res != nil && strings.EqualFold(res.Status, "awaiting_confirmation") &&
				strings.TrimSpace(res.DraftID) != "" {
				confirm := Inbound{
					Platform: "lanxin", BotID: h.cfg.BotID, BotName: h.cfg.BotName,
					PlatformMessageID: m.MsgID + ":auto-confirm", ChatType: m.ChatType,
					SenderID: m.From, SenderName: m.SenderName, GroupID: m.GroupID,
					GroupName: m.GroupName, ConversationID: m.ConversationID, MsgType: m.MsgType,
					Text: "确认建单 " + res.DraftID, IsAtMe: true,
				}
				if derr := h.disp.Dispatch(ctx, confirm); derr != nil {
					log.Printf("[bot] auto confirm crash draft %s failed: %v", res.DraftID, derr)
				} else {
					msg = "崩溃已解析，正在自动建单；Issue 链接会发到本群。"
				}
			}
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
		return h.dispatchMedia(ctx, m, attachmentCaption(m), ids)
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
		return h.dispatch(ctx, m, text, false, nil)
	case "group":
		if nonDirect && !observeAll {
			h.reportInbox(ctx, m, "群消息未 @ 机器人")
			return nil
		}
		if text == "/new" {
			h.reportInbox(ctx, m, "命令已在本地处理")
			return h.reply(ctx, m, "已开启新会话：此前的上下文记忆已重置。")
		}
		return h.dispatch(ctx, m, text, true, nil)
	default:
		h.reportInbox(ctx, m, "未知会话类型")
		log.Printf("[bot] unknown chatType=%q msgId=%s groupId=%s", m.ChatType, m.MsgID, m.GroupID)
		return nil
	}
}

func (h *Handler) dispatch(ctx context.Context, m *lanxin.InboundMessage, text string, isGroup bool, attachments []Attachment) error {
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
		ReplyToBot:        m.ReferenceMsg != nil && m.ReferenceMsg.FromType == 1,
		Attachments:       attachments,
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

const (
	maxBotAttachments     = 4
	maxBotAttachmentBytes = 8 << 20
)

// dispatchMedia 下载媒体并连同说明文字一起派单；下载失败也要让模型知道。
func (h *Handler) dispatchMedia(ctx context.Context, m *lanxin.InboundMessage, text string, mediaIDs []string) error {
	atts, note, err := h.collectAttachments(ctx, m, mediaIDs)
	if err != nil {
		log.Printf("[bot] attachment download failed msgId=%s: %v", m.MsgID, err)
	}
	if len(atts) == 0 && strings.TrimSpace(text) == "" {
		return h.reply(ctx, m, "收到附件，但下载失败或格式不支持，请补充一段文字说明。")
	}
	if note != "" {
		text = strings.TrimSpace(strings.TrimSpace(text) + "\n" + note)
	}
	isGroup := m.ChatType == "group"
	return h.dispatch(ctx, m, text, isGroup, atts)
}

// collectAttachments 按 mediaId 下载附件，限制数量和单文件大小。
func (h *Handler) collectAttachments(ctx context.Context, m *lanxin.InboundMessage, mediaIDs []string) ([]Attachment, string, error) {
	if h.media == nil {
		return nil, "（未配置媒体下载，附件未随消息发送）", nil
	}
	out := make([]Attachment, 0, len(mediaIDs))
	for i, id := range mediaIDs {
		if len(out) >= maxBotAttachments {
			return out, "（附件超过数量上限，其余已忽略）", nil
		}
		data, err := h.media.DownloadMedia(ctx, id)
		if err != nil {
			return out, "（部分附件下载失败）", err
		}
		if len(data) > maxBotAttachmentBytes {
			return out, "（附件超过单文件大小上限，已忽略）", nil
		}
		mimeType := mimeBaseType(http.DetectContentType(data))
		out = append(out, Attachment{Name: attachmentName(m, i, mimeType), MimeType: mimeType, Data: data})
	}
	return out, "", nil
}

// inboundMediaMeta 是入站消息各媒体段里与展示相关的字段。
type inboundMediaMeta struct {
	Content  string `json:"content"`
	Name     string `json:"name"`
	FileName string `json:"fileName"`
}

// displayName 兼容不同网关版本的文件名字段。
func (m inboundMediaMeta) displayName() string {
	if name := strings.TrimSpace(m.Name); name != "" {
		return name
	}
	return strings.TrimSpace(m.FileName)
}

// attachmentCaption 提取图片/文件/语音/视频消息自带的说明文字。
func attachmentCaption(m *lanxin.InboundMessage) string {
	if text := strings.TrimSpace(m.TextContent()); text != "" {
		return text
	}
	var d struct {
		Image inboundMediaMeta `json:"image"`
		File  inboundMediaMeta `json:"file"`
		Voice inboundMediaMeta `json:"voice"`
		Video inboundMediaMeta `json:"video"`
	}
	if err := json.Unmarshal(m.MsgData, &d); err != nil {
		return ""
	}
	for _, part := range []inboundMediaMeta{d.Image, d.File, d.Voice, d.Video} {
		if v := strings.TrimSpace(part.Content); v != "" {
			return v
		}
	}
	return ""
}

// attachmentName 取文件名；没有文件名时按内容类型补扩展名。
func attachmentName(m *lanxin.InboundMessage, index int, mimeType string) string {
	var d struct {
		File inboundMediaMeta `json:"file"`
	}
	_ = json.Unmarshal(m.MsgData, &d)
	if name := d.File.displayName(); name != "" {
		return name
	}
	ext := extensionForMime(mimeType)
	if ext == "" {
		ext = ".bin"
	}
	return fmt.Sprintf("attachment-%d%s", index+1, ext)
}

// isCrashBundle 判断这条附件消息是不是崩溃上报。
func isCrashBundle(m *lanxin.InboundMessage, text string) bool {
	if strings.EqualFold(strings.TrimSpace(text), "/crash") {
		return true
	}
	var d struct {
		File inboundMediaMeta `json:"file"`
	}
	_ = json.Unmarshal(m.MsgData, &d)
	return strings.HasSuffix(strings.ToLower(d.File.displayName()), "crash_bundle.json")
}

// mimeBaseType 去掉 Content-Type 参数，例如 text/plain; charset=utf-8 -> text/plain。
func mimeBaseType(mimeType string) string {
	base, _, _ := strings.Cut(mimeType, ";")
	return strings.TrimSpace(base)
}

func extensionForMime(mimeType string) string {
	switch strings.ToLower(mimeBaseType(mimeType)) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "application/json":
		return ".json"
	}
	return ""
}
