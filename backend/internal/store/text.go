package store

import (
	"strings"
	"unicode/utf8"
)

// sanitizeText 去掉 PostgreSQL 文本列无法接受的 NUL 字节，并把非法 UTF-8
// 换成替换字符。构建日志、命令输出和事件详情都可能带二进制碎片，一条坏
// 记录不应该让整轮发布的写入失败。
func sanitizeText(value string) string {
	if !strings.ContainsRune(value, 0) && utf8.ValidString(value) {
		return value
	}
	value = strings.ReplaceAll(value, "\x00", "")
	return strings.ToValidUTF8(value, "\uFFFD")
}
