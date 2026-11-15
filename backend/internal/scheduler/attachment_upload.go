package scheduler

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
)

type issueAttachmentRef struct {
	name string
	url  string
}

// issueAttachments 是一次建单/补充操作里上传成功的图片与失败项。
type issueAttachments struct {
	images   []issueAttachmentRef
	failures []string
}

// uploadIssueImages 把消息里的图片上传到 GitHub。只处理 image/*；日志、配置等
// 文件仍然只提供给模型阅读，避免把无关文件挂到 Issue 上。上传失败不阻断建单。
func (s *Scheduler) uploadIssueImages(ctx context.Context, p *Project, msg Inbound) issueAttachments {
	var out issueAttachments
	uploader, ok := s.tracker.(AttachmentUploader)
	if !ok || p == nil {
		return out
	}
	uploaded := 0
	for i, a := range msg.Attachments {
		if len(a.Data) == 0 {
			continue
		}
		mimeType := strings.TrimSpace(a.MimeType)
		if mimeType == "" {
			mimeType = http.DetectContentType(a.Data)
		}
		mimeType = mimeBase(mimeType)
		if !strings.HasPrefix(mimeType, "image/") {
			continue
		}
		name := chatAttachmentName(a.Name, i, mimeType)
		if uploaded >= maxChatAttachments {
			out.failures = append(out.failures, name+"（超过数量上限）")
			continue
		}
		if len(a.Data) > maxChatAttachmentBytes {
			out.failures = append(out.failures, name+"（超过 8 MB）")
			continue
		}
		url, err := uploader.UploadAttachment(ctx, p.ID, name, mimeType, a.Data)
		if err != nil {
			log.Printf("[scheduler] upload chat image %s for issue failed: %v", name, err)
			out.failures = append(out.failures, name)
			continue
		}
		uploaded++
		out.images = append(out.images, issueAttachmentRef{name: name, url: url})
	}
	return out
}

// apply 把上传结果写进 Issue 正文或评论：模型按文件名引用过的链接先替换成真实
// URL，未引用的图片追加到“聊天截图”一节，失败项用引用块提示人工补充。
func (a issueAttachments) apply(body string) string {
	for _, img := range a.images {
		for _, ref := range []string{"](" + img.name + ")", "](./" + img.name + ")"} {
			body = strings.ReplaceAll(body, ref, "]("+img.url+")")
		}
	}
	if len(a.images) > 0 {
		var b strings.Builder
		b.WriteString("## 聊天截图\n")
		for _, img := range a.images {
			fmt.Fprintf(&b, "\n![%s](%s)\n", img.name, img.url)
		}
		body = appendMarkdownBlock(body, b.String())
	}
	if len(a.failures) > 0 {
		body = appendMarkdownBlock(body, "> 附件上传失败："+
			strings.Join(a.failures, "、")+"；请在 Issue 中手动补充。")
	}
	return body
}

func appendMarkdownBlock(body, block string) string {
	if strings.TrimSpace(body) == "" {
		return block
	}
	return strings.TrimRight(body, " \t\r\n") + "\n\n" + block
}
