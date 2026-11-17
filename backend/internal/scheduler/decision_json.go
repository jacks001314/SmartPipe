package scheduler

import (
	"encoding/json"
	"fmt"
	"strings"
)

// unmarshalModelJSONObject 解析模型输出的 JSON 对象。
//
// 模型偶尔会在 JSON 字符串字面量里直接输出换行、制表符等控制字符
// （例如把多行 reply/body 原样贴进字符串），encoding/json 会报
// "invalid character '\n' in string literal" 并让整条消息处理失败。
// 首次解析失败时，把字符串内部的原始控制字符转义后重试一次；
// 仍失败则返回首次解析的错误，保持原有错误语义。
func unmarshalModelJSONObject(payload string, v any) error {
	firstErr := json.Unmarshal([]byte(payload), v)
	if firstErr == nil {
		return nil
	}
	repaired := escapeControlCharsInJSONStrings(payload)
	if repaired == payload {
		return firstErr
	}
	if err := json.Unmarshal([]byte(repaired), v); err == nil {
		return nil
	}
	return firstErr
}

// escapeControlCharsInJSONStrings 只处理 JSON 字符串字面量内部的控制字符，
// 字符串外的缩进/换行保持原样。合法的转义序列（\\"、\\\\ 等）不会被重复转义。
func escapeControlCharsInJSONStrings(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 16)
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			if escaped {
				b.WriteByte(c)
				escaped = false
				continue
			}
			switch c {
			case '\\':
				b.WriteByte(c)
				escaped = true
			case '"':
				b.WriteByte(c)
				inString = false
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				if c < 0x20 {
					fmt.Fprintf(&b, `\u%04x`, c)
				} else {
					b.WriteByte(c)
				}
			}
			continue
		}
		if c == '"' {
			inString = true
		}
		b.WriteByte(c)
	}
	return b.String()
}
