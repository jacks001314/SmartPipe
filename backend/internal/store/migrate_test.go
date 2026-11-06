package store

import "testing"

// 全新数据库必须从 version 1 开始建表；声明顺序（新增在前）不能影响执行顺序。
func TestOrderedMigrationsAscending(t *testing.T) {
	ordered := orderedMigrations()
	if len(ordered) != len(migrations) {
		t.Fatalf("迁移数量不一致：%d != %d", len(ordered), len(migrations))
	}
	for i := 1; i < len(ordered); i++ {
		if ordered[i].version <= ordered[i-1].version {
			t.Fatalf("迁移未按升序排列：%d 在 %d 之后", ordered[i].version, ordered[i-1].version)
		}
	}
	if ordered[0].name != "core_schema" {
		t.Fatalf("第一条迁移应为 core_schema，实际是 %s", ordered[0].name)
	}
}
