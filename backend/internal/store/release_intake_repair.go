package store

// 早期 desktop 构建把 desktop_test_* 记录成迁移 44-47。从那种库升级上来的
// 数据库会跳过 45（release_intake），导致发版入口缺表。用一个新的唯一版本
// 重新执行同一份幂等 DDL，不修改历史记录。
func init() {
	migrations = append(migrations, migration{
		version: 53,
		name:    "repair_release_intake_after_desktop_collision",
		sql:     releaseIntakeSQL,
	})
}
