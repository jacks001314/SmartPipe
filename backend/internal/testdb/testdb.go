// Package testdb 统一集成测试的数据库选择。
//
// 这些测试会清空 projects、bridge_links 等表，一旦 DSN 指到开发或生产库就是灾难。
// 所以只接受名字以 _test 结尾的库，其余直接判失败而不是"帮忙跑一下"。
package testdb

import (
	"net/url"
	"os"
	"strings"
	"testing"
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
		t.Fatalf("SMARTPIPE_TEST_DSN 必须指向 *_test 库（当前 %q），这些测试会清空业务表", name)
	}
	return dsn
}

func databaseName(dsn string) string {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		if u, err := url.Parse(dsn); err == nil && u.Path != "" {
			return strings.TrimPrefix(u.Path, "/")
		}
	}
	// key=value 形式：取 dbname= 的值。
	for _, field := range strings.Fields(dsn) {
		if strings.HasPrefix(field, "dbname=") {
			return strings.TrimPrefix(field, "dbname=")
		}
	}
	if i := strings.LastIndex(dsn, "/"); i >= 0 {
		return dsn[i+1:]
	}
	return dsn
}