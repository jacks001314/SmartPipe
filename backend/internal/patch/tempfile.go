package patch

import (
	"os"
	"path/filepath"
)

// writeTempPatch 把补丁写到系统临时目录，返回路径与清理函数。
func writeTempPatch(patchText string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "smartpipe-patch-*")
	if err != nil {
		return "", func() {}, err
	}
	file := filepath.Join(dir, "change.diff")
	if err := os.WriteFile(file, []byte(NormalizePatch(patchText)), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", func() {}, err
	}
	return file, func() { _ = os.RemoveAll(dir) }, nil
}
