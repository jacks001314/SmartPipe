package adminapi

import (
	"fmt"
	"strconv"
	"strings"
)

// majorConfirmError 表示发版命中"大版本需要人工确认"。
type majorConfirmError struct {
	Last string
	Next string
}

func (e *majorConfirmError) Error() string {
	return fmt.Sprintf("版本 %s 相对上次发布 %s 是大版本升级，需要人工确认（重新提交时带 confirm_major=true）", e.Next, e.Last)
}

// majorOf 解析 vX.Y.Z / X.Y 的主版本号；无法解析时返回 ok=false。
func majorOf(version string) (int, bool) {
	v := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(version)), "v"))
	if v == "" {
		return 0, false
	}
	// 预发布/构建后缀不参与比较：2.0.0-rc.1 -> 2.0.0
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) == 0 || parts[0] == "" {
		return 0, false
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, false
	}
	return n, true
}

// needsMajorConfirm 判断 next 是否是相对 last 的大版本升级。
func needsMajorConfirm(last, next string) bool {
	lastMajor, okLast := majorOf(last)
	nextMajor, okNext := majorOf(next)
	if !okLast || !okNext {
		return false
	}
	// 日历版本（v2026.11.05）不属于 semver 大版本，不做人工确认。
	if nextMajor >= 100 || lastMajor >= 100 {
		return false
	}
	return nextMajor > lastMajor
}
