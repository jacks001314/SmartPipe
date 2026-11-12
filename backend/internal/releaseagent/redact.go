package releaseagent

import "regexp"

var redactPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`rmt_[0-9a-fA-F]{16,}`), "rmt_***"},
	{regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{16,}`), "gh*_***"},
	{regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]{12,}`), "Bearer ***"},
	{regexp.MustCompile(`(?i)("?(?:api[_-]?key|token|secret|password|credential)"?\s*[:=]\s*"?)[A-Za-z0-9._\-]{12,}`), "${1}***"},
}

// Redact 去掉事件与错误信息里的令牌样式字符串，保证控制台与日志可分享。
func Redact(text string) string {
	if text == "" {
		return ""
	}
	for _, p := range redactPatterns {
		text = p.re.ReplaceAllString(text, p.repl)
	}
	return text
}
