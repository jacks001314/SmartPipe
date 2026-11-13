# 本地命令场景

worker 配置可增加 commandScenario，对应 CommandScenarioConfig；设置后运行命令场景而非观察场景。
场景部署在受控测试机上，后端只选择 name@version，不传执行命令。

配置结构：
- name/version：场景身份。
- stateRoot：与该桌面其他Runner共用的绝对状态目录。
- artifactPath：本地候选制品绝对路径，执行前匹配任务SHA256。
- workingDirectory：隔离场景工作目录。
- scripts：path与sha256列表，执行前校验所有部署脚本。
- setup：安装/准备命令列表（可选）。
- checks：独立检查命令列表，id须覆盖后端required_checks。
- cleanup：必须配置，停止目标进程并验证清理成功。
每个命令包含 id、executable绝对路径、args数组、timeoutMs（100..600000）。
执行使用shell:false，不展开任务文本，不继承机器API令牌。
脚本及其完整依赖应由运维部署为只读；当前只校验显式列出的文件，不能宣称实现依赖闭包或系统级沙箱。

注入命令环境：
SMARTPIPE_TEST_RUN_ID / SMARTPIPE_TEST_ARTIFACT / SMARTPIPE_TEST_ARTIFACT_SHA256 /
SMARTPIPE_TEST_SOURCE_SHA / SMARTPIPE_TEST_OUTPUT。
脚本用这些字段绑定被测安装和报告，不应把密钥写到stdout/stderr；输出会限长保存在证据中。

setup非零后检查标记BLOCKED；检查exit 0为PASS、非零为FAIL。
检查脚本必须实际验证独立事实，不能只打印成功或信任模型自述。
cleanup成功后才释放本地锁和上传结果；失败/取消/超时保持机器待恢复。
终止直接子进程不等于整棵进程树已清理，cleanup必须停止并核验所有目标进程；平台级Job Object/进程组隔离尚未实现。

真实执行测试使用自建文件夹具，验证安装复制、独立读回、清理、非零退出和超时。
尚无Syntropy完整安装/编码/升级场景配置。接入时至少提供：
1. 安装指定哈希的候选包；
2. 读出实际运行版本并验证与制品一致；
3. UI/Agent执行工作；
4. 独立文件/后端状态断言；
5. 清理并验证无残留。
只有这些检查完成后，才应为该场景配置项目发布门禁。
