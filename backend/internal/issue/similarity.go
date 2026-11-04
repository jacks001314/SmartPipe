package issue

import (
	"strings"
	"unicode"
)

// bigramSet 生成字符二元组集合（大小写归一，剔除空白/标点）。
// 对中文短文本的相似检索足够好用，向量召回留给后续（见设计文档 9.2 P2）。
func bigramSet(s string) map[string]struct{} {
	var sb strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(r)
		}
	}
	runes := []rune(sb.String())
	set := make(map[string]struct{}, max(0, len(runes)-1))
	for i := 0; i+1 < len(runes); i++ {
		set[string(runes[i:i+2])] = struct{}{}
	}
	return set
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Containment 返回短文本被长文本覆盖的程度：|A∩B| / min(|A|,|B|)。
// 空文本返回 0；完全一致返回 1。适合"一句报障 vs 一段 Issue 描述"的比较。
func Containment(a, b string) float64 {
	sa, sb := bigramSet(a), bigramSet(b)
	if len(sa) == 0 || len(sb) == 0 {
		if a == b && a != "" {
			return 1
		}
		return 0
	}
	small, big := sa, sb
	if len(sb) < len(sa) {
		small, big = sb, sa
	}
	hit := 0
	for g := range small {
		if _, ok := big[g]; ok {
			hit++
		}
	}
	return float64(hit) / float64(len(small))
}
