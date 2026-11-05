// Package crash 负责崩溃包（crash_bundle）的校验、脱敏、指纹计算与自动建单。
package crash

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ErrInvalid 表示崩溃包缺少必要字段或超出限制。
var ErrInvalid = errors.New("crash: invalid bundle")

const (
	// MaxStackBytes 限制调用栈长度。
	MaxStackBytes = 64 * 1024
	// MaxMessageBytes 限制异常消息长度。
	MaxMessageBytes = 4 * 1024
	// MaxLogs 保留的日志行数。
	MaxLogs = 100
	// MaxLogLineBytes 单行日志长度。
	MaxLogLineBytes = 2 * 1024
)

// Exception 是异常摘要。
type Exception struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Stack   string `json:"stack"`
}

// Bundle 是客户端上报的崩溃包。
type Bundle struct {
	Fingerprint string            `json:"fingerprint"`
	App         string            `json:"app"`
	Version     string            `json:"version"`
	Build       string            `json:"build"`
	OS          string            `json:"os"`
	Arch        string            `json:"arch"`
	UserID      string            `json:"user_id"`
	DeviceID    string            `json:"device_id"`
	OccurredAt  string            `json:"occurred_at"`
	Exception   Exception         `json:"exception"`
	Logs        []string          `json:"logs"`
	Metadata    map[string]string `json:"metadata"`
	Reports     int               `json:"reports"`
}

// Normalized 是脱敏、截断后的崩溃包。
type Normalized struct {
	Bundle      Bundle
	Fingerprint string
	OccurredAt  time.Time
	SizeBytes   int
}

var (
	tokenPatterns = []*regexp.Regexp{
		regexp.MustCompile(`ghp_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
		regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		regexp.MustCompile(`sk-[A-Za-z0-9]{16,}`),
		regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]{12,}`),
		regexp.MustCompile(`(?i)(password|passwd|token|secret|api[_-]?key|cookie)\s*[:=]\s*[^\s,;]+`),
	}
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	winHome      = regexp.MustCompile(`(?i)[A-Z]:\\Users\\[^\\\s"']+`)
	nixHome      = regexp.MustCompile(`/(?:home|Users)/[^/\s"']+`)
	hexAddr      = regexp.MustCompile(`0x[0-9a-fA-F]{6,}`)
	lineSuffix   = regexp.MustCompile(`(?i)(?::|at\s+)line\s+\d+|:\d+:\d+`)
)

// Redact 对文本做凭据、邮箱与用户目录脱敏。
func Redact(text string) string {
	out := text
	for _, re := range tokenPatterns {
		out = re.ReplaceAllString(out, "[REDACTED]")
	}
	out = emailPattern.ReplaceAllString(out, "[email]")
	out = winHome.ReplaceAllString(out, "<home>")
	out = nixHome.ReplaceAllString(out, "<home>")
	return out
}

func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n...[truncated]"
}

// Normalize 校验并脱敏崩溃包，返回指纹与发生时间。
func (b *Bundle) Normalize(now time.Time) (*Normalized, error) {
	b.App = strings.TrimSpace(b.App)
	b.Version = strings.TrimSpace(b.Version)
	b.Build = strings.TrimSpace(b.Build)
	b.OS = strings.TrimSpace(b.OS)
	b.Arch = strings.TrimSpace(b.Arch)
	b.UserID = strings.TrimSpace(b.UserID)
	b.DeviceID = strings.TrimSpace(b.DeviceID)
	b.Exception.Type = strings.TrimSpace(b.Exception.Type)
	b.Exception.Message = strings.TrimSpace(b.Exception.Message)

	if b.App == "" {
		return nil, fmt.Errorf("%w: app is required", ErrInvalid)
	}
	if b.Exception.Message == "" && b.Exception.Type == "" {
		return nil, fmt.Errorf("%w: exception type or message is required", ErrInvalid)
	}
	if len(b.Exception.Stack) > MaxStackBytes {
		return nil, fmt.Errorf("%w: stack exceeds %d bytes", ErrInvalid, MaxStackBytes)
	}
	if len(b.Logs) > 2000 {
		return nil, fmt.Errorf("%w: too many log lines", ErrInvalid)
	}

	scrubbed := Scrub(*b)
	*b = scrubbed

	occurred := now.UTC()
	if strings.TrimSpace(b.OccurredAt) != "" {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(b.OccurredAt)); err == nil {
			occurred = t.UTC()
		}
	}
	fingerprint := strings.TrimSpace(b.Fingerprint)
	if fingerprint == "" {
		fingerprint = ComputeFingerprint(*b)
	} else if !strings.HasPrefix(fingerprint, "sha256:") {
		fingerprint = "sha256:" + fingerprint
	}
	b.Fingerprint = fingerprint

	raw, _ := json.Marshal(*b)
	return &Normalized{Bundle: *b, Fingerprint: fingerprint, OccurredAt: occurred, SizeBytes: len(raw)}, nil
}

// ComputeFingerprint 用应用名、异常类型和栈的前几帧计算稳定指纹。
func ComputeFingerprint(b Bundle) string {
	var frames []string
	for _, line := range strings.Split(b.Exception.Stack, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = hexAddr.ReplaceAllString(line, "0xADDR")
		line = lineSuffix.ReplaceAllString(line, "")
		frames = append(frames, line)
		if len(frames) == 3 {
			break
		}
	}
	key := strings.ToLower(b.App) + "|" + strings.ToLower(b.Exception.Type) + "|" + strings.Join(frames, "|")
	sum := sha256.Sum256([]byte(key))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ShortFingerprint 返回可读的短指纹。
func ShortFingerprint(fp string) string {
	fp = strings.TrimPrefix(fp, "sha256:")
	if len(fp) > 12 {
		return fp[:12]
	}
	return fp
}

// Title 生成 Issue 标题。
func Title(b Bundle) string {
	summary := b.Exception.Type
	if b.Exception.Message != "" {
		if summary != "" {
			summary += ": "
		}
		summary += b.Exception.Message
	}
	summary = strings.ReplaceAll(summary, "\n", " ")
	title := "[crash] " + b.App + " " + summary
	if len(title) > 120 {
		title = title[:120]
	}
	return title
}

// Body 生成 Issue 正文。
func Body(n Normalized, source string, reportID int64, occurrences int, now time.Time) string {
	b := n.Bundle
	var sb strings.Builder
	sb.WriteString("## 崩溃摘要\n\n")
	sb.WriteString(fmt.Sprintf("- 应用：%s %s (%s)\n", b.App, b.Version, b.Build))
	sb.WriteString(fmt.Sprintf("- 环境：%s / %s\n", b.OS, b.Arch))
	sb.WriteString(fmt.Sprintf("- 用户：%s · 设备：%s\n", orDash(b.UserID), orDash(b.DeviceID)))
	sb.WriteString(fmt.Sprintf("- 发生时间：%s\n", n.OccurredAt.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("- 指纹：`%s`（去重键）\n", n.Fingerprint))
	sb.WriteString(fmt.Sprintf("- 上报：crash_report #%d · 来源 %s · 第 %d 次出现 · 收到于 %s\n", reportID, orDash(source), occurrences, now.UTC().Format(time.RFC3339)))
	sb.WriteString("\n## 异常\n\n")
	sb.WriteString("`" + b.Exception.Type + ": " + firstLine(b.Exception.Message) + "`\n\n")
	if strings.TrimSpace(b.Exception.Stack) != "" {
		sb.WriteString("```\n" + strings.TrimSpace(b.Exception.Stack) + "\n```\n")
	}
	if len(b.Logs) > 0 {
		sb.WriteString("\n## 最近日志\n\n```\n")
		sb.WriteString(strings.Join(b.Logs, "\n"))
		sb.WriteString("\n```\n")
	}
	if len(b.Metadata) > 0 {
		sb.WriteString("\n## 元数据\n\n")
		keys := make([]string, 0, len(b.Metadata))
		for k := range b.Metadata {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sb.WriteString(fmt.Sprintf("- `%s`：%s\n", k, b.Metadata[k]))
		}
	}
	sb.WriteString("\n## 处理说明\n\n")
	sb.WriteString("- 本 Issue 由崩溃上报自动创建，内容已脱敏；请勿回贴用户文件正文或未脱敏日志。\n")
	sb.WriteString("- 复杂修复仍需在评论区确认方案后再进入开发。\n")
	return sb.String()
}

// DupComment 生成重复出现的评论。
func DupComment(n Normalized, occurrences int, reportID int64, now time.Time) string {
	b := n.Bundle
	return fmt.Sprintf("再次出现：第 %d 次（crash_report #%d，%s，版本 %s，%s）。\n\n指纹：`%s`",
		occurrences, reportID, now.UTC().Format(time.RFC3339), orDash(b.Version), orDash(b.OS), n.Fingerprint)
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}

// Scrub 对任意崩溃包做脱敏与截断，供拒绝记录复用。
func Scrub(b Bundle) Bundle {
	b.Exception.Type = Redact(truncateBytes(b.Exception.Type, 200))
	b.Exception.Message = Redact(truncateBytes(b.Exception.Message, MaxMessageBytes))
	b.Exception.Stack = Redact(truncateBytes(b.Exception.Stack, MaxStackBytes))
	b.Version = truncateBytes(b.Version, 64)
	b.Build = truncateBytes(b.Build, 64)
	b.OS = truncateBytes(b.OS, 128)
	b.Arch = truncateBytes(b.Arch, 32)
	b.UserID = truncateBytes(b.UserID, 128)
	b.DeviceID = truncateBytes(b.DeviceID, 128)
	logs := make([]string, 0, MaxLogs)
	for _, line := range b.Logs {
		logs = append(logs, Redact(truncateBytes(line, MaxLogLineBytes)))
	}
	if len(logs) > MaxLogs {
		logs = logs[len(logs)-MaxLogs:]
	}
	b.Logs = logs
	if len(b.Metadata) > 0 {
		clean := make(map[string]string, len(b.Metadata))
		for k, v := range b.Metadata {
			clean[truncateBytes(k, 64)] = Redact(truncateBytes(v, 1024))
		}
		b.Metadata = clean
	}
	return b
}
