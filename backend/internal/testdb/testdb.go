// Package testdb 统一集成测试的数据库选择。
// 测试会清空业务表，所以只接受驱动实际解析出的 *_test 数据库。
package testdb

import (
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// DSN 返回可安全清理的测试库连接串；未配置时跳过，指错库时失败。
func DSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("SMARTPIPE_TEST_DSN"))
	if dsn == "" {
		t.Skip("SMARTPIPE_TEST_DSN 未设置，跳过集成测试")
	}
	name := databaseName(dsn)
	if !strings.HasSuffix(name, "_test") {
		t.Fatal("SMARTPIPE_TEST_DSN 必须有效且指向 *_test 库，这些测试会清空业务表")
	}
	return dsn
}

func databaseName(dsn string) string {
	// 与 store 使用同一个驱动解析，尊重 URL 查询覆盖、引用和重复键语义。
	// 不输出解析错误：错误可能包含连接串中的凭据。
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return ""
	}
	return config.Database
}
