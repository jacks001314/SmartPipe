# 测试数据库 DSN 校验修复

Linux Agent 静态指出原手工解析与 pgx 实际解析不一致。已改为使用 store 同版本 pgx.ParseConfig 获取 Database；解析失败拒绝，且错误不输出连接串。

已运行 `go test ./internal/testdb -count=1 -v`（进程内设置 GOSUMDB=sum.golang.org），退出码 0。TestDatabaseName 下 10 个子用例全部通过，无跳过：包含 URL dbname 覆盖、重复 keyword 最后值生效、引用值及无效连接串。

文件 backend/internal/testdb/testdb.go 和 testdb_test.go 已写回 Master。

此测试不连接数据库，不证明迁移44–47通过。Linux Docker权限与子 Agent 项目关联仍待解决；已建议通过正式贡献 checkout 获取源码并检查独立用户态 PostgreSQL 是否可用。
