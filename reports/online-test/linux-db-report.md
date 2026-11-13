# SmartPipe Linux 真实 PostgreSQL 测试报告（更新）

## 结论

2026-11-13，smartpipe-linux-test 在 jacks Linux 使用 **PostgreSQL 18.3** 独立用户级临时实例完成真实 DB 测试；不是 PG17 验收。store/testlab/releasemcp 全包通过；adminapi 仍有两个旧测试失败，不能宣称四包全部通过。

## 来源与环境

附件 linux-source.zip SHA256：33f888bb24d240438e3eba77108465bb9a6b155bffd48b29869c5f106257082e。SOURCE-MANIFEST.json 的 286 文件逐一 SHA256 校验全部通过。初次 unzip 的中文文件名解码不正确，改用 Python 按 ZIP 编码恢复后再次校验通过。

Go 自动工具链：go1.26.10 linux/amd64。服务器 `SELECT version()` 返回 PostgreSQL 18.3 (Debian 18.3-1+b1), x86_64-pc-linux-gnu, gcc 15.2.0, 64-bit。

每轮均 mktemp 创建全新集群，Unix socket 位于 0700 临时目录，禁用 TCP；本地 trust 只用于该隔离目录，host reject，用户 sp_test、数据库 smartpipe_test。无密码生成或打印，无业务数据库连接。Docker 权限未改变。

## 复测结果（顶层测试计数，子用例另见 counts.json）

|包|通过|失败|跳过|
|---|---:|---:|---:|
|store|18|0|0|
|testlab|3|0|0|
|adminapi|33|2|0|
|releasemcp|14|0|0|
|testdb|1|0|0|

全包复测共69通过、2失败、0跳过。testdb 是解析单测，不能视作真实 DB 用例；四包中也存在纯逻辑测试，不把69项全部宣称为数据库测试。

另独立执行 `-run TestDesktop`：store 5通过、0失败、0跳过；其他三包无匹配测试（不是跳过）。全部按包串行、`-count=1 -timeout=180s`，完整事件日志保留实际用例名。

## 迁移实证

每轮测试后直接 SQL 查询 schema_migrations：44 desktop_test_runs、45 desktop_test_machines、46 desktop_test_evidence、47 desktop_gate_policy 四行均存在，applied_at 见 commands.log。全新实例迁移成功，多次测试重复 Migrate 也通过。

## 失败与隔离问题

1. TestCommitProxiesToVerifierAndRecords：422，`/tmp/repo` 不存在。
2. TestPushInjectsCredentialWithoutLeaking：422，`/tmp/repo` 不存在。

测试仍假定请求转发给 fakeVerifier，但 mergeapi.go 当前调用本地 patch.Verifier。属于既有 adminapi 测试与实现不匹配；不应通过创建共享 /tmp/repo 或改业务行为强行使之通过。该文件不在本 Agent store/testlab* 修改职责内，报告父 Agent 修复。store/testlab* 未发现需修复缺陷，源码没有修改。

首轮 adminapi 为32通过、3失败，额外失败 TestModelConfigLocalPreviewReportsMissingFile：测试设置 CODEX_HOME 却未隔离 HOME，应用意外回退读取用户配置并输出配置摘要。没有主动读取凭据文件，未回显密钥值；该测试的输出已整体脱敏替换。此隔离不足应如实记录，不能声称首轮完全未接触用户配置。第二轮使用全新空 HOME、CODEX_HOME、SMARTPIPE_CODEX_CONFIG 路径，并清除 PG 环境变量、禁用全局 Git 配置；该测试通过，其余两项仍失败。没有再读取真实用户配置，也未返回含配置摘要的原始日志。

## 清理与交付

两轮均 pg_ctl fast stop 成功、server stopped；仅删除本轮 mktemp 数据/socket 目录，commands.log 均记录 CLEANUP=True；postgres.log 记录数据库关闭。没有容器或业务库变更。

完整脱敏首轮/复测命令、Go JSON 日志、服务器日志、计数以及复现 run.py 打包为 linux-db-evidence.zip；通过 A2A 附件回传，避免当前 project_* 关联不一致造成父 Agent 无法读取。无源码补丁。
