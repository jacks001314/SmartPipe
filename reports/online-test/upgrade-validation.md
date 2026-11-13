# 旧版数据库升级验证

新增迁移52：旧桌面版本使用44后，会跳过上游release_v2_executor_hosts；52以幂等DDL补建该表，不重写历史迁移记录。

TestDesktopLegacyMigrationUpgrade 在独立 *_test 数据库重建旧迁移历史44–47，保留已有desktop表与generation=7的机器，执行两次迁移后检查主机表存在、机器代次未变、新迁移48–52完整。

Linux PostgreSQL18.3 实跑：store19/adminapi32/releasemcp14/testlab3/testdb1/patch13，82顶层测试全部通过，无失败无跳过。日志见upgrade-retest；临时实例停止并清理成功。这仅证明数据库/后端测试，不代表桌面产品或发布验收完成。
