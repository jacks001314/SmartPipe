# 合并后迁移冲突修复

main 9442848 实际在 Linux PostgreSQL18.3 上复测，发现上游 release_v2_executor_hosts 与 desktop_test_runs 共享44，导致桌面表未创建。现将在线测试迁移改为48–51，并使用幂等建表/加列语句兼容此前已建桌面表的开发实例。未修改业务数据库。

真实隔离实例复测：store18、adminapi32、releasemcp14、testlab3、testdb1、patch13，合计81个顶层测试通过，0失败0跳过。日志位于 fixed-retest。此前两项 Git API fixture 已通过。

临时 PostgreSQL 集群已停止并清理；测试源码与日志目录保留。旧迁移44–47报告是历史快照，不适用于合并后的版本。PG17、旧版完整升级路径、Runner服务端端到端、产品Windows/macOS验收尚待验证。

连接：jacks@192.168.0.5 SSH现已成功；malama@192.168.0.3 SSH成功，macOS12.4 x86_64，默认PATH未发现Node。Windows Qwen原生依赖安装下载超时，MCP协议预检成功不代表原生驱动可用。
