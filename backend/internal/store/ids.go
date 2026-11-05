package store

import (
	"crypto/rand"
	"encoding/hex"
)

// newID 生成带前缀的随机标识，例如 prj_9f2c1a4b。
func newID(prefix string) string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 失败属于系统级故障，用固定前缀兜底避免返回空 ID。
		return prefix + "_00000000"
	}
	return prefix + "_" + hex.EncodeToString(buf)
}
