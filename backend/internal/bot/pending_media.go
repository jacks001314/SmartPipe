package bot

import (
	"strings"
	"time"

	"smartpipe/sdks/lanxin"
)

// pendingMediaTTL 是“先发图、再 @ 机器人”的衔接窗口：图片消息和触发消息
// 之间只隔一句话，10 分钟足够覆盖，又不会把很久以前的图误挂到新 Issue。
const pendingMediaTTL = 10 * time.Minute

type pendingImages struct {
	attachments []Attachment
	at          time.Time
}

func pendingKey(chatID, userID string) string { return chatID + "|" + userID }

// rememberRecentImages 缓存消息里的图片，供同一发送者紧接着的文本消息使用。
// 典型场景：同事先发两张参考截图（没 @ 机器人），再补一句“@XTianTian 建单”。
func (h *Handler) rememberRecentImages(chatID, userID string, atts []Attachment) {
	images := make([]Attachment, 0, len(atts))
	for _, a := range atts {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(a.MimeType)), "image/") && len(a.Data) > 0 {
			images = append(images, a)
		}
	}
	if len(images) == 0 {
		return
	}
	now := time.Now()
	h.mediaMu.Lock()
	defer h.mediaMu.Unlock()
	if h.pendingImg == nil {
		h.pendingImg = map[string]pendingImages{}
	}
	key := pendingKey(chatID, userID)
	merged := images
	if prev, ok := h.pendingImg[key]; ok && now.Sub(prev.at) <= pendingMediaTTL {
		merged = append(append([]Attachment{}, prev.attachments...), images...)
	}
	if len(merged) > maxBotAttachments {
		merged = merged[len(merged)-maxBotAttachments:]
	}
	h.pendingImg[key] = pendingImages{attachments: merged, at: now}
	h.prunePendingImagesLocked(now)
}

func (h *Handler) prunePendingImagesLocked(now time.Time) {
	for k, v := range h.pendingImg {
		if now.Sub(v.at) > pendingMediaTTL {
			delete(h.pendingImg, k)
		}
	}
}

// takeRecentImages 取走并清空缓存，避免同一批截图被重复挂到多个 Issue。
func (h *Handler) takeRecentImages(chatID, userID string, now time.Time) []Attachment {
	chatID = strings.TrimSpace(chatID)
	userID = strings.TrimSpace(userID)
	if chatID == "" || userID == "" {
		return nil
	}
	h.mediaMu.Lock()
	defer h.mediaMu.Unlock()
	key := pendingKey(chatID, userID)
	entry, ok := h.pendingImg[key]
	if !ok {
		return nil
	}
	delete(h.pendingImg, key)
	if now.Sub(entry.at) > pendingMediaTTL {
		return nil
	}
	return entry.attachments
}

// recentImages 是文本消息的入口；命令类消息不携带历史图片。
func (h *Handler) recentImages(m *lanxin.InboundMessage, explicitCommand bool) []Attachment {
	if m == nil || explicitCommand {
		return nil
	}
	chatID := strings.TrimSpace(m.GroupID)
	if chatID == "" {
		chatID = m.From
	}
	return h.takeRecentImages(chatID, m.From, time.Now())
}
