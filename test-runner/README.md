# SmartPipe Desktop Test Runner

Runner 包含 stdio MCP 网关、一次性 HTTP Worker 和本地命令场景。后端负责测试任务、机器认证、租约、证据、报告及发布门禁；管理台提供任务创建、证据下载和受控恢复。

## 构建

要求 Node.js 24+：

```text
npm ci --ignore-scripts
npm test
```

所需 Qwen Node REPL/CUA 模块位于 modules，使用私有 @smartpipe 包名，保留各模块 LICENSE、NOTICE 和 UPSTREAM 来源记录。只抽取所需模块，不引入完整 Qwen CLI。原生运行时固定 0.27.0，按 modules 部署说明校验归档哈希并离线安装。

## MCP 网关

```text
node ABSOLUTE_PATH/dist/src/cli.js ABSOLUTE_STATE_ROOT -- ABSOLUTE_MCP_EXECUTABLE [args...]
```

由 MCP 客户端通过 stdin/stdout 启动，诊断写 stderr。上游 executable 及脚本使用绝对路径，不在会话中通过 npx 下载。网关保留 MCP 图片与结构化输出，工具目录分页发现，动作串行执行，错误不自动重试。

控制同一桌面的实例必须共用受控 state root。文件锁和20分钟期限是协作机制，Node REPL 不是操作系统沙箱。日志不复制输入参数或工具文本，截图可能包含应用数据。manifest 标记 NOT_EVALUATED；工具调用成功不是产品验收通过。

## HTTP Worker

```text
node dist/src/worker-cli.js worker-config.json
```

每次领取一条任务，完成返回 reported，无任务返回 idle。配置示例见 worker.example.json；命令场景配置见 COMMAND_SCENARIOS.md。

由操作员通过部署环境注入 SMARTPIPE_TEST_MACHINE_TOKEN，不能使用管理员令牌。管理身份通过 POST /admin/test-machines 登记机器，机器令牌只返回一次，数据库保存摘要。Runner 子进程不继承机器令牌。

生产连接使用 HTTPS；仅回环地址允许 HTTP。机器 ID 使用 X-Test-Machine-ID，认证使用 Bearer 机器令牌。Worker 调用 /test-runner/claim、heartbeat、evidence、report、release。

场景选择：
- desktop-observe@1：控件与截图观察，只用于连通性诊断，不作为发布验收。
- commandScenario：部署的 name@version，校验候选制品和显式脚本哈希，执行 setup/checks/cleanup。后端只选择能力，不下发任意命令。命令场景按需加载，无需桌面 MCP 依赖。

领取后每15秒续租，服务端租约60秒；续租失败、租约接近到期或20分钟总期限到达会中止。先清理，再上传证据、提交报告，最后释放机器。失败、响应丢失、取消或强杀不自动重试，不自动重派忙机器。

## 受控恢复

先停用机器并确认本地任务及其所有子进程已停止、清理完成，再通过管理台恢复，填写原因并提交当前 generation 和 cleanup_confirmed=true。服务端要求机器停用且代次匹配，恢复递增代次，机器仍保持停用。核对后再显式启用。

取消后的旧 Worker 不能用普通 release 替代管理员恢复，也不能在副作用不明确时重发点击。相关实现位于 backend/internal/adminapi/testlab_machine_admin.go。

## 证据和门禁

证据经过哈希校验并与运行绑定。管理台下载接口提供原字节，禁止缓存；报告检查引用上传证据。门禁要求项目、源码提交、制品摘要、场景版本、平台及必测项匹配。观察场景不能满足发布验收。

命令输出限长后作为证据保存。检查脚本必须独立验证事实，不能只打印成功。脚本及依赖应受控部署；当前只校验显式列出的文件。清理脚本负责停止并核验整个目标进程树。

## 已验证和剩余工作

2026-11-13：Runner 21项测试通过；Linux隔离PostgreSQL16六包111项通过，零失败、零跳过。实际CLI执行了夹具脚本并通过HTTP完成领取、证据、报告、释放和门禁验证，见 ../reports/online-test/command-cli-verification.md。

Windows原生库加载通过；现有macOS12.4不能加载要求macOS13的固定原生库。这些结果不证明桌面产品业务验收通过。

仍需可追溯候选包、独立可控实例和实际安装/业务/重启升级/清理场景。尚无常驻服务安装器、自动循环调度、通用制品下载器或机器令牌轮换。本工具不能绕过桌面控制策略。

## 上游

- https://github.com/QwenLM/qwen-code/tree/84b5b53f1738ef154bca3b3bd8bb40f4f3378510/packages/cua-driver
- https://github.com/QwenLM/qwen-code/tree/84b5b53f1738ef154bca3b3bd8bb40f4f3378510/packages/node-repl

分发时保留各组件原有许可及通知；模块抽取记录见 modules 内的来源文件。
