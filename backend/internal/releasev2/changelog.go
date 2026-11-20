package releasev2

import (
	"fmt"
	"regexp"
	"strings"
)

const changelogHeading = "## 更新日志"

// ChangelogCommit 是生成更新日志所需的最小提交信息。
type ChangelogCommit struct {
	SHA     string
	Message string
}

type changelogGroup struct {
	title string
	items []string
	seen  map[string]bool
}

var conventionalCommitPattern = regexp.MustCompile(`^([A-Za-z]+)(?:\(([^)]*)\))?!?:\s*(.+)$`)

// RenderChangelog 把两个版本之间的提交整理成面向用户的更新日志。
// 测试、构建、发布一类的提交不展开，同一标题只保留一次；perGroup 控制
// 每个分组最多列多少条，避免一次长周期发布把 Release 正文撑得过长。
func RenderChangelog(previousTag, tag, compareURL string, commits []ChangelogCommit, perGroup int) string {
	if perGroup <= 0 {
		perGroup = 25
	}
	groups := []*changelogGroup{
		{title: "新功能", seen: map[string]bool{}},
		{title: "修复", seen: map[string]bool{}},
		{title: "优化", seen: map[string]bool{}},
		{title: "文档", seen: map[string]bool{}},
		{title: "其他", seen: map[string]bool{}},
	}
	total := 0
	for _, commit := range commits {
		subject := firstLine(commit.Message)
		if subject == "" {
			continue
		}
		total++
		group, item, ok := changelogEntry(subject)
		if !ok || group < 0 || group >= len(groups) {
			continue
		}
		key := strings.ToLower(item)
		if groups[group].seen[key] {
			continue
		}
		groups[group].seen[key] = true
		groups[group].items = append(groups[group].items, item)
	}

	var b strings.Builder
	b.WriteString(changelogHeading)
	b.WriteString("\n\n")
	if total > 0 {
		fmt.Fprintf(&b, "共 %d 个提交；以下按类型列出面向用户的改动（测试、构建与发布提交不展开）。\n", total)
	}
	written := 0
	totalItems := 0
	for _, group := range groups {
		totalItems += len(group.items)
		if len(group.items) == 0 {
			continue
		}
		b.WriteString("\n### ")
		b.WriteString(group.title)
		b.WriteString("\n\n")
		limit := len(group.items)
		if limit > perGroup {
			limit = perGroup
		}
		// 提交按时间正序返回；正文优先展示最新的改动。
		for i := len(group.items) - 1; i >= len(group.items)-limit; i-- {
			b.WriteString("- ")
			b.WriteString(group.items[i])
			b.WriteString("\n")
		}
		written += limit
	}
	if written == 0 {
		b.WriteString("\n本版本没有需要展开的功能性改动。\n")
	} else if written < totalItems {
		b.WriteString("\n> 条目过多，已省略部分提交；完整差异见下方链接。\n")
	}
	if compareURL != "" {
		b.WriteString("\n")
		if previousTag != "" && tag != "" {
			fmt.Fprintf(&b, "[完整变更：%s → %s](%s)\n", previousTag, tag, compareURL)
		} else {
			fmt.Fprintf(&b, "[完整变更](%s)\n", compareURL)
		}
	}
	return strings.TrimSpace(b.String())
}

// MergeChangelog 在 Agent 重写发布说明时保住已有的更新日志段。
// 只有原说明带「## 更新日志」、新说明没带时才补回，避免覆盖人工写好的内容。
func MergeChangelog(existing, updated string) string {
	section := extractChangelogSection(existing)
	if section == "" || strings.Contains(updated, changelogHeading) {
		return updated
	}
	updated = strings.TrimSpace(updated)
	if updated == "" {
		return section
	}
	return updated + "\n\n" + section
}

func changelogEntry(subject string) (int, string, bool) {
	subject = strings.TrimSpace(subject)
	lower := strings.ToLower(subject)
	if strings.HasPrefix(lower, "merge ") || strings.HasPrefix(lower, "revert \"") {
		return 0, "", false
	}
	match := conventionalCommitPattern.FindStringSubmatch(subject)
	if match == nil {
		if strings.HasPrefix(lower, "release:") || strings.HasPrefix(lower, "release ") {
			return 0, "", false
		}
		return 4, subject, true
	}
	kind := strings.ToLower(match[1])
	scope := strings.TrimSpace(match[2])
	text := strings.TrimSpace(match[3])
	if text == "" {
		return 0, "", false
	}
	item := text
	if scope != "" {
		item = scope + "：" + text
	}
	switch kind {
	case "feat":
		return 0, item, true
	case "fix", "revert":
		return 1, item, true
	case "perf", "refactor", "opti", "optimize":
		return 2, item, true
	case "docs":
		return 3, item, true
	case "test", "tests", "chore", "ci", "build", "style", "release", "merge", "wip", "deps":
		return 0, "", false
	default:
		return 4, item, true
	}
}

func extractChangelogSection(markdown string) string {
	idx := strings.Index(markdown, changelogHeading)
	if idx < 0 {
		return ""
	}
	rest := markdown[idx+len(changelogHeading):]
	if cut := strings.Index(rest, "\n## "); cut >= 0 {
		rest = rest[:cut]
	}
	return strings.TrimSpace(changelogHeading + rest)
}

func firstLine(text string) string {
	if idx := strings.IndexAny(text, "\r\n"); idx >= 0 {
		text = text[:idx]
	}
	return strings.TrimSpace(text)
}
