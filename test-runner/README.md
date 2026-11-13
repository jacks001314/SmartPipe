# SmartPipe Desktop Test Runner — M1

当前交付是本机 stdio MCP 网关，尚不是完整测试调度平台。TypeScript 网关包装预安装的 Qwen CUA 原生 MCP 或 Node REPL MCP，无需引入 Qwen Agent。

## 构建与检查

要求 Node.js 24+。在本目录执行：

```text
npm ci --ignore-scripts
npm test
npm audit
```

## 启动

```text
node ABSOLUTE_PATH/dist/src/cli.js ABSOLUTE_STATE_ROOT -- ABSOLUTE_MCP_EXECUTABLE [args...]
```

例如上游是原生 Qwen 驱动时，末尾参数是 `mcp`；上游是 Node REPL 时，使用 Node 的绝对路径作为 executable，预安装 node-repl 的 dist/index.js 绝对路径作为参数。不在测试会话中调用 npx 下载。

这是 MCP 服务，必须由 MCP 客户端启动，通过 stdin/stdout 通信。诊断只写 stderr。现有 Go codexrunner.Spec.ConfigOverrides 已有 MCP 配置覆盖接入点，CLI 自动配置与远程转发尚待实现。

Qwen 上游固定评估版本 0.27.0；包版本已通过 npm registry 查询确认，但此提交没有安装原生运行时，也没有进行真实桌面截图或输入验证。按照上游安装文档在专用测试机部署，完成系统权限授权后再接入。网关不会绕过上游权限策略。

## 已实现

- 上游工具目录分页发现及调用代理，保留 MCP 图片和结构化结果。
- 同一个 state root 下文件独占锁、20 分钟会话期限、动作串行执行。
- 工具错误不重试；外部副作用可能已发生，应先观察再决定。
- 每轮随机运行目录，保存 manifest、actions.jsonl、截图和 SHA-256。
- 操作日志不复制输入参数或工具文本；截图仍可能包含应用数据。
- 会话退出时关闭上游并释放锁；强制终止留下锁时默认拒绝新任务，须确认旧进程已停止再由运维清理。

所有控制同一桌面的实例必须使用相同的受控 state root。文件锁是本地协作机制，不是安全边界；Node REPL 也不是操作系统沙箱。当前不应面向非受信任任务开放。

## 验证范围

npm test 的跨进程 MCP 测试使用自建上游夹具，验证图片转发和证据保存，不能证明 Qwen 原生驱动、Windows/macOS 桌面或 Syntropy 业务路径已通过。

工具会话的 manifest 始终标记 NOT_EVALUATED。没有独立验收器时，不能把工具成功调用提升为产品 PASS。

## 后续里程碑

1. 在专用 Windows/Mac 上部署固定 Qwen 运行时，验证观察、点击、中文输入、取消及权限缺失。
2. 后端持久化测试机、租约、任务，加入认证的反向连接、心跳与取消。
3. 接入候选安装包及哈希、Syntropy 场景、独立验收检查。
4. 接入发布门禁和管理台；未通过的必测项必须阻止发布。

## 上游与许可

- https://github.com/QwenLM/qwen-code/tree/84b5b53f1738ef154bca3b3bd8bb40f4f3378510/packages/cua-driver
- https://github.com/QwenLM/qwen-code/tree/84b5b53f1738ef154bca3b3bd8bb40f4f3378510/packages/node-repl

当前网关没有复制 Qwen 源码。部署或再分发上游组件时保留其各自 LICENSE/NOTICE；Node REPL 为 Apache-2.0，CUA SDK 包声明 MIT，其他原生及 vendored 组件按实际分发内容核对。

## 远程 Worker（新增）

`node dist/src/worker-cli.js worker-config.json` 每次领取并执行一条任务，没任务返回 idle。
配置样例见 worker.example.json；Mac 使用该机器实际的绝对路径。
运行前由操作员通过部署环境注入 SMARTPIPE_TEST_MACHINE_TOKEN，不能使用管理员令牌。
上游 Node REPL 子进程不会继承此机器令牌。

控制端先通过管理身份调用 POST /admin/test-machines 登记平台，响应只返回一次机器令牌，数据库保存 SHA256。
生产连接必须 HTTPS；本机开发允许 localhost/127.0.0.1 HTTP。
Runner 使用 /test-runner/claim、heartbeat、report、release，机器 ID 来自 X-Test-Machine-ID。

当前仅实现 desktop-observe 场景版本 1，必测项可设 desktop_observed。
该场景校验配置指定文件的哈希，读取已运行应用的控件和截图，不安装应用，也不证明当前运行的应用就是该文件安装出来的版本。
因此结果只能用于桌面连通性诊断，不能作为发布验收。
不要向此 Worker 分配其他场景：不支持时会失败并保留占用等待恢复。

领取任务后每 15 秒续租，服务端租约 60 秒；续租失败或接近到期立即中止当前场景并关闭上游。
报告发送前先确认清理完成，报告被接受后才释放机器。
任务失败、响应丢失、进程强杀后不会自动重试，也不会自动重新分配忙机器。
恢复流程：确认本地进程已退出 → 管理端取消未结束运行 → 用相同机器身份和 generation 调用 release。
不能在不知道原操作是否执行的情况下重发点击。

目前只实现一次性 worker，尚无常驻服务安装器、自动循环调度、制品下载/安装、可信证据上传和机器令牌轮换。
