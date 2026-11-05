// Package patch 实现补丁协议校验：base_commit 绑定、路径白名单、大小与行数限制、
// 二进制/密钥扫描，以及干净副本上的 git apply 与验证命令执行。
package patch

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Bundle 是 dev 回传给 lead 的补丁包。
type Bundle struct {
	IssueID     string            `json:"issue_id"`
	Repo        string            `json:"repo"`
	BaseCommit  string            `json:"base_commit"`
	Patch       string            `json:"patch"`
	TestLog     string            `json:"test_log"`
	Environment map[string]string `json:"environment"`
	Author      string            `json:"author"`
}

// Limits 控制补丁的规模上限。
type Limits struct {
	MaxPatchBytes   int
	MaxChangedLines int
}

// DefaultLimits 对应方案 T-015：1MB 或 500 行。
func DefaultLimits() Limits {
	return Limits{MaxPatchBytes: 1 << 20, MaxChangedLines: 500}
}

// DefaultDenyPrefixes 是禁止补丁触碰的路径前缀。
var DefaultDenyPrefixes = []string{
	".github/workflows/",
	".git/",
	"secrets/",
}

// ErrInvalidPatch 表示补丁未通过结构化校验。
var ErrInvalidPatch = errors.New("patch: invalid")

var (
	privateKeyRe = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)
	githubPATRe  = regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`)
	githubFineRe = regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`)
	awsKeyRe     = regexp.MustCompile(`AKIA[0-9A-Z]{16}`)
	slackRe      = regexp.MustCompile(`xox[abprs]-[A-Za-z0-9-]{10,}`)
	openAIKeyRe  = regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`)
)

// NormalizePatch 统一换行并保证结尾换行，避免不同工作站生成不可应用的补丁。
func NormalizePatch(patchText string) string {
	text := strings.ReplaceAll(patchText, "\r\n", "\n")
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text
}

// PatchHash 返回补丁正文（规范化后）的 sha256 指纹。
func PatchHash(patchText string) string {
	sum := sha256.Sum256([]byte(NormalizePatch(patchText)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ChangedFiles 解析 unified diff 涉及的文件路径（新路径；删除文件用旧路径）。
func ChangedFiles(patchText string) ([]string, error) {
	text := strings.ReplaceAll(patchText, "\r\n", "\n")
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("%w: patch is empty", ErrInvalidPatch)
	}
	var (
		files []string
		seen  = map[string]bool{}
		oldIn string
	)
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || p == "/dev/null" || seen[p] {
			return
		}
		seen[p] = true
		files = append(files, p)
	}
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			rest := strings.TrimPrefix(line, "diff --git ")
			_, b, ok := strings.Cut(rest, " b/")
			if ok {
				add(b)
			}
		case strings.HasPrefix(line, "--- "):
			oldIn = strings.TrimSpace(strings.TrimPrefix(line, "--- "))
			oldIn = strings.TrimPrefix(oldIn, "a/")
		case strings.HasPrefix(line, "+++ "):
			p := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			if p == "/dev/null" {
				add(oldIn)
			} else {
				add(strings.TrimPrefix(p, "b/"))
			}
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%w: no file paths found in patch", ErrInvalidPatch)
	}
	return files, nil
}

// ChangedLineCount 统计新增/删除行数（不含文件头）。
func ChangedLineCount(patchText string) int {
	n := 0
	for _, line := range strings.Split(strings.ReplaceAll(patchText, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			n++
		}
	}
	return n
}

// Validate 结构化校验补丁；失败返回 ErrInvalidPatch 包装。
func Validate(b Bundle, limits Limits, denyPrefixes []string) ([]string, error) {
	if strings.TrimSpace(b.BaseCommit) == "" {
		return nil, fmt.Errorf("%w: base_commit is required", ErrInvalidPatch)
	}
	if !regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`).MatchString(strings.TrimSpace(b.BaseCommit)) {
		return nil, fmt.Errorf("%w: base_commit must be a hex git commit", ErrInvalidPatch)
	}
	if limits.MaxPatchBytes <= 0 {
		limits.MaxPatchBytes = DefaultLimits().MaxPatchBytes
	}
	if limits.MaxChangedLines <= 0 {
		limits.MaxChangedLines = DefaultLimits().MaxChangedLines
	}
	if len(b.Patch) > limits.MaxPatchBytes {
		return nil, fmt.Errorf("%w: patch is %d bytes, limit %d", ErrInvalidPatch, len(b.Patch), limits.MaxPatchBytes)
	}
	text := NormalizePatch(b.Patch)
	if strings.Contains(text, "GIT binary patch") || strings.Contains(text, "Binary files ") {
		return nil, fmt.Errorf("%w: binary patch is not allowed", ErrInvalidPatch)
	}
	if n := ChangedLineCount(text); n > limits.MaxChangedLines {
		return nil, fmt.Errorf("%w: %d changed lines, limit %d", ErrInvalidPatch, n, limits.MaxChangedLines)
	}
	files, err := ChangedFiles(text)
	if err != nil {
		return nil, err
	}
	if denyPrefixes == nil {
		denyPrefixes = DefaultDenyPrefixes
	}
	for _, f := range files {
		if err := checkPath(f, denyPrefixes); err != nil {
			return nil, err
		}
	}
	if secret := scanSecrets(text); secret != "" {
		return nil, fmt.Errorf("%w: possible secret in patch (%s)", ErrInvalidPatch, secret)
	}
	return files, nil
}

func checkPath(p string, deny []string) error {
	clean := strings.ReplaceAll(p, "\\", "/")
	if strings.HasPrefix(clean, "/") || regexp.MustCompile(`^[A-Za-z]:/`).MatchString(clean) {
		return fmt.Errorf("%w: absolute path %q", ErrInvalidPatch, p)
	}
	if clean != path.Clean(clean) || strings.Contains(clean, "../") || clean == ".." {
		return fmt.Errorf("%w: path traversal %q", ErrInvalidPatch, p)
	}
	for _, prefix := range deny {
		if strings.HasPrefix(clean, prefix) {
			return fmt.Errorf("%w: path %q is denied (%s)", ErrInvalidPatch, p, prefix)
		}
	}
	return nil
}

func scanSecrets(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "+") || strings.HasPrefix(line, "+++") {
			continue
		}
		switch {
		case privateKeyRe.MatchString(line):
			return "private key"
		case githubPATRe.MatchString(line):
			return "github token"
		case githubFineRe.MatchString(line):
			return "github fine-grained token"
		case awsKeyRe.MatchString(line):
			return "aws access key"
		case slackRe.MatchString(line):
			return "slack token"
		case openAIKeyRe.MatchString(line):
			return "openai key"
		}
	}
	return ""
}
