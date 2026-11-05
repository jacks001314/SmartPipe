package crash

import (
	"fmt"
	"sort"
	"strings"
)

// Similar 描述一条与本次崩溃相似的既有上报。
type Similar struct {
	ReportID    int64   `json:"report_id"`
	IssueNumber int     `json:"issue_number"`
	IssueURL    string  `json:"issue_url"`
	Fingerprint string  `json:"fingerprint"`
	Version     string  `json:"version"`
	Score       float64 `json:"score"`
}

// frameKeys 把调用栈归一成函数签名集合：去掉地址与行号，只保留前 n 行。
func frameKeys(stack string, n int) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(stack, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = hexAddr.ReplaceAllString(line, "0xADDR")
		line = lineSuffix.ReplaceAllString(line, "")
		out[strings.ToLower(line)] = true
		if len(out) >= n {
			break
		}
	}
	return out
}

// StackSimilarity 返回两个崩溃包的相似度（0..1，Jaccard）。
func StackSimilarity(a, b Bundle) float64 {
	fa := frameKeys(a.Exception.Stack, 6)
	fb := frameKeys(b.Exception.Stack, 6)
	if len(fa) == 0 || len(fb) == 0 {
		return 0
	}
	if !strings.EqualFold(strings.TrimSpace(a.Exception.Type), strings.TrimSpace(b.Exception.Type)) {
		// 异常类型不同但栈一致的情况很常见（包装异常），给一个折扣。
		shared := 0
		for k := range fa {
			if fb[k] {
				shared++
			}
		}
		if shared < 2 {
			return 0
		}
	}
	shared := 0
	for k := range fa {
		if fb[k] {
			shared++
		}
	}
	union := len(fa) + len(fb) - shared
	if union == 0 {
		return 0
	}
	score := float64(shared) / float64(union)
	if !strings.EqualFold(strings.TrimSpace(a.Exception.Type), strings.TrimSpace(b.Exception.Type)) {
		// 包装异常很常见，但类型不同时只作为参考，降低权重。
		score *= 0.8
	}
	return score
}

// topSimilar 过滤并排序候选。
func topSimilar(items []Similar, min float64, limit int) []Similar {
	out := make([]Similar, 0, len(items))
	for _, it := range items {
		if it.Score >= min {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// SimilarSection 生成 Issue 正文里的"可能相关"段落。
func SimilarSection(items []Similar) string {
	if len(items) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n## 可能相关的问题\n\n")
	for _, it := range items {
		url := strings.TrimSpace(it.IssueURL)
		if url == "" {
			url = fmt.Sprintf("#%d", it.IssueNumber)
		} else {
			url = fmt.Sprintf("[#%d](%s)", it.IssueNumber, url)
		}
		sb.WriteString(fmt.Sprintf("- %s · 相似度 %.0f%% · 版本 %s · 指纹 `%s`\n",
			url, it.Score*100, orDash(it.Version), ShortFingerprint(it.Fingerprint)))
	}
	sb.WriteString("\n> 仅按调用栈相似度自动检索，供分诊参考；合并前请人工确认根因。\n")
	return sb.String()
}
