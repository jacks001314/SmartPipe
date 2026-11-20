# Syntropy 测试引擎：设计与落地

日期：2026-11-18

## 1. 结论

测试引擎不是"又一个制品门禁"，而是运行在测试机上的**本地测试服务**：

- 内核是一个本地 Codex Agent，直接使用测试机的 shell、文件、进程、桌面和产品接口；
- 外层是 Supervisor，负责领取租约、物化输入、应用/回滚热替换、超时清理、独立证据和上报；
- **源码、制品、故事、热替换计划全部由 SmartPipe 调度准备**，引擎不执行 `git clone`、不解析分支、不自行下载源码；
- 引擎对上游暴露作业接口（输入包、故事、检查、findings、证据、结果），对 Agent 暴露 MCP 工具面；
- 控制台提供故事库：导入/编辑/预览 Markdown，选择模式一键触发，查看检查项、问题列表和输入包。

发布门禁仍然只认确定性 Test Run（`source_sha + artifact_sha256 + scenario@version`）。故事型/缺陷型/热替换型测试产出的是可审阅结论与证据，不被引擎或 Agent 自报的 PASS 直接当作门禁。

## 2. Syntropy 真机事实（已在 192.168.197.108 核对）

### 2.1 安装布局

`C:\Users\windows\AppData\Local\Programs\Syntropy\resources`：

| 路径 | 内容 | 可替换性 |
|---|---|---|
| `bin/win32-x64/syntropy-master.exe` | Master（mbot） | 可换，需重启 Master |
| `bin/win32-x64/syntropy-app-server.exe` | 桌面 sidecar / Web 后端 | 可换，需重启 App Server |
| `bin/win32-x64/syntropy-browser-*.exe` | 浏览器桥/宿主 | 可换 |
| `node/artifacts/nbot-<os>-<arch>` | Node 执行面 | 可换，重下发后生效 |
| `node/artifacts/mbot-<os>-<arch>` | Master 制品来源 | 可换 |
| `node/artifacts/codex-*.tgz` | Codex 运行时 | 随 nbot 整包覆盖 |
| `node/artifacts/node-runtime-*`、`node-repl-*`、`computer-use-*` | 运行时组件 | 随 nbot 整包覆盖 |
| `node/conf/nbot.json.tpl`、`mbot.json.tpl` | 运行时配置模板 | 可换 |
| `app.asar` | Electron 主/渲染代码 | **不热替换**：生产构建有 ASAR 完整性校验 |
| `release-manifest.json` | 每组件 `size + sha256`，codex 带 `integrity` | 只读校验依据 |

运行进程链（实测）：

```
Syntropy.exe
  └─ syntropy-app-server.exe --server-addr 127.0.0.1:0 --node-dir ...\resources\node ...
       └─ mbot.exe supervise --config %USERPROFILE%\.syntropy\master\.syntropy-run-master\conf\mbot.json
            └─ mbot.exe run --config ...
                 └─ nbot.exe supervise --config %USERPROFILE%\.syntropy-run-<hash>\conf\nbot.json
                      └─ nbot.exe run --config ...
```

### 2.2 App Server 的隔离与替换开关

`syntropy-app-server.exe --help` 实测支持：

| 参数 | 用途 | 测试价值 |
|---|---|---|
| `--server-addr 127.0.0.1:0` | 随机端口 | 不占用真实安装版的端口 |
| `--node-dir <dir>` | 组件目录（模板 + artifacts） | 可指向一份**完整隔离副本** |
| `--node-artifacts-dir <dir>` | 额外制品目录，**优先于** `node-dir/artifacts` | 不碰安装目录即可换 nbot/codex 等 |
| `--master-data-dir` | Master 数据种子目录 | 与真实 `~/.syntropy` 隔离 |
| `--master-run-root` | 本地 Master 运行根 | 每次测试独立 |
| `--credential-dir` / `--log-dir` | 凭据与日志 | 独立证据 |
| `--desktop-token` / `--desktop-handshake` | 会话令牌 / stdout 握手 | 自动化启动 |

因此两条热替换路径是明确的：

1. **隔离模式（推荐用于 bug/story）**：复制 `resources/node` 到 job 目录，使用 `--node-dir` + `--node-artifacts-dir` + 独立 `master-data-dir` 启动 App Server，部署本地 Master 后执行故事。全程不改真实安装。
2. **原位模式（用于验证安装版本身）**：按计划停进程、备份、替换 `bin/win32-x64/*.exe` 或 `node/artifacts/*`、启动、健康检查；失败按策略回滚。

### 2.3 可复用的 Syntropy 测试资产

| 资产 | 位置 | 引擎如何使用 |
|---|---|---|
| 稳定性场景 JSON | `desktop_app/tools/stability/scenarios/*.json` | 作为故事中的命令目标或输入包内容 |
| Windows 冒烟脚本 | `desktop_app/tools/smoke-*.ps1` | 故事直接调用，Agent 记录输出与截图 |
| Playwright 规格 | `desktop_app/tests/e2e/*.spec.ts` | 需要测试构建时由调度准备 |
| 质量门禁 | `npm run verify` / `test:quality-gates` / `test:boundaries` | 适合作为源码工作区里的快速检查 |
| App Server 自动化 | `desktop_app/tools/stability/lib/app-server.mjs` | 参数与握手方式的参考实现 |

## 3. 职责边界

```
SmartPipe 调度 / 控制台
  1. 拉取源码（scheduler 负责）并冻结为不可变包
  2. 准备制品 / patch / 故事 / 热替换计划
  3. 创建 Test Session，声明输入包 name + sha256 + 解包方式 + 目标路径
  4. 上传输入包（PUT /admin/test-sessions/{id}/inputs/{name}）
        │
Windows 测试机
  Test Engine（本服务）
  1. 领取会话租约
  2. 下载输入包、校验 sha256、安全解包
  3. 解析故事（front matter + 必测项）
  4. 应用热替换计划（备份/校验/健康检查）
  5. 启动本地 Codex Agent（codex exec + MCP）
  6. 持续同步检查与 findings；超时杀进程树
  7. 回滚热替换；采集最终截图/进程快照/transcript
  8. 提交 checks、findings、verdict、summary；释放机器
        │
  Codex Agent（本地 shell + 产品接口 + MCP）
```

引擎不做的事：不 clone 源码、不选分支、不修改调度输入、不把机器令牌传给 Agent 子进程、不把自报 PASS 当门禁。

## 4. 输入契约

`SessionSpec.engine`：

```json
{
  "inputs": [
    {"name":"workspace","kind":"workspace","sha256":"<64 hex>","bytes":123,
     "unpack":"tar.gz","target":"workspace"},
    {"name":"stories","kind":"stories","sha256":"<64 hex>","unpack":"zip","target":"stories"},
    {"name":"plan","kind":"hotswap","sha256":"<64 hex>","unpack":"none","target":"hotswap/plan.json"}
  ],
  "stories": [
    {"input":"stories","path":"hotswap-smoke.md","required_checks":["component_restored"]}
  ],
  "hotswap": {"input":"plan","path":"plan.json","restore_policy":"always"},
  "agent": {"sandbox":"danger-full-access","timeout_seconds":1800},
  "env": {"SYNTROPY_ROOT":"C:\\Users\\windows\\AppData\\Local\\Programs\\Syntropy"},
  "workdir": "workspace"
}
```

约束：

- `name`：`^[a-z][a-z0-9_-]{0,31}$`，单会话最多 16 个；
- `sha256` 必填，引擎下载后重新计算，不一致立即 BLOCKED；
- `target` 必须是 job 目录内的相对路径；归档条目拒绝绝对路径、`..` 与符号链接；
- 故事路径相对输入根，front matter 支持 `id/title/mode/environment/platform/required_checks/tags/timeout_seconds`；
- 纯故事模式可以没有输入包，此时引擎使用空的 job workspace。

模式：`story | bug | explore | hotswap | gate`。`hotswap` 表示必须执行替换计划后再跑故事；`gate` 仅表示沿用确定性门禁语义（门禁判定不在引擎里）。

## 5. 热替换计划

计划是**显式步骤**，引擎不猜产品怎么启动：

```json
{
  "schema": 1,
  "name": "syntropy-nbot-0.5.22",
  "install_root": "C:\\Users\\windows\\AppData\\Local\\Programs\\Syntropy",
  "allow_asar": false,
  "steps": [
    {"op":"note","note":"replace node artifact then redeploy"},
    {"op":"copy","source":"artifacts/nbot-windows-amd64.exe",
     "target":"resources/node/artifacts/nbot-windows-amd64.exe","sha256":"<64 hex>"},
    {"op":"stop_process","name":"nbot"},
    {"op":"start_process","executable":"C:\\...\\nbot.exe","args":["run","--config","..."]},
    {"op":"wait_http","url":"http://127.0.0.1:3333/health","status":[200],"timeout_ms":30000}
  ]
}
```

支持的操作：`copy`、`remove`、`stop_process`、`start_process`、`wait_http`、`exec`、`sleep`、`note`。

`stop_process` 支持 `tree: true` 结束子进程树，并等待进程真正退出后再继续（Windows 上
镜像文件在句柄释放前无法覆盖）。计划可声明 `restore_steps`：恢复备份后按顺序执行，
用于把被停掉的产品重新拉起，例如：

```json
{
  "schema": 1, "name": "swap-and-restart",
  "steps": [{"op":"stop_process","name":"Syntropy","tree":true},
            {"op":"copy","source":"artifacts/x.css","target":"resources/plugins/quant/ui/styles.css"},
            {"op":"start_process","executable":"C:\\...\\Syntropy.exe",
             "args":["--remote-debugging-port=9334","--remote-allow-origins=*"]},
            {"op":"wait_http","url":"http://127.0.0.1:9334/json/version","status":[200]}],
  "restore_steps": [{"op":"stop_process","name":"Syntropy","tree":true},
                    {"op":"start_process","executable":"C:\\...\\Syntropy.exe",
                     "args":["--remote-debugging-port=9334","--remote-allow-origins=*"]},
                    {"op":"wait_http","url":"http://127.0.0.1:9334/json/version","status":[200]}]
}
```

保证：

- `copy/remove` 前自动备份到 `job/hotswap-backup/`，记录原文件是否存在；
- 目标必须落在 `install_root` 内；`app.asar` 默认拒绝，除非计划显式 `allow_asar: true`（并清楚这会触发完整性校验失败）；
- 失败或 `restore_policy=always` 时：先结束计划启动的进程并等待退出，再逆序恢复文件，
  最后执行 `restore_steps` 恢复运行态；恢复步骤的成败同样进入事件流；
- 计划步骤、哈希、失败原因全部进入事件流和最终证据。

## 6. 引擎接口

### 6.1 本地 HTTP（仅回环，job token）

| 方法 | 路径 | 作用 |
|---|---|---|
| GET | `/v1/health` | 存活 |
| GET | `/v1/jobs` | 作业列表（control token） |
| GET | `/v1/jobs/{id}` | 作业上下文、输入哈希、故事、检查、findings |
| GET | `/v1/jobs/{id}/stories` | 故事索引 |
| GET | `/v1/jobs/{id}/stories/{sid}` | 故事全文 |
| POST | `/v1/jobs/{id}/checks` | 记录/覆盖检查 |
| POST | `/v1/jobs/{id}/findings` | 记录/覆盖问题 |
| POST | `/v1/jobs/{id}/evidence` | 文件或 base64 证据，返回 `sha256:` |
| POST | `/v1/jobs/{id}/capture` | 截取窗口/全屏并登记证据 |
| POST | `/v1/jobs/{id}/events` | 进度事件 |
| POST | `/v1/jobs/{id}/finish` | 提交 PASS/FAIL/BLOCKED 与摘要 |

### 6.2 Agent MCP 工具

`engine_job`、`engine_story_list`、`engine_story_read`、`engine_check_record`、`engine_finding_report`、`engine_evidence_upload`、`engine_evidence_capture`、`engine_log`、`engine_finish`。

Agent 的执行能力来自 Codex 原生 shell/文件/进程工具，不再需要经后端长轮询的 `env_*` 代理。旧的 `Test Session` 远程命令通道保留为兼容模式。

### 6.3 SmartPipe 接口（本次新增）

| 方法 | 路径 | 作用 |
|---|---|---|
| PUT | `/admin/test-sessions/{id}/inputs/{name}` | 调度上传输入包（sha256 与声明必须一致） |
| GET | `/admin/test-sessions/{id}/inputs` | 输入清单 |
| GET | `/admin/test-sessions/{id}/evidence` | 证据清单（sha256/mime/bytes，不含载荷） |
| GET | `/admin/test-sessions/{id}/evidence/{digest}` | 按摘要读取证据载荷 |
| GET | `/test-runner/session/inputs/{id}?generation=` | 引擎下载清单（机器令牌） |
| GET | `/test-runner/session/inputs/{id}/{name}?generation=` | 引擎下载包 |
| POST | `/test-runner/session/check` | 运行中同步检查 |
| POST | `/test-runner/session/finding` | 运行中同步 findings |
| POST | `/test-runner/session/report` | 最终提交 checks + findings + verdict |
| GET/POST/PUT/DELETE | `/admin/test-stories...` | 故事库 CRUD 与导入 |
| POST | `/admin/test-stories/{id}/run` | 从故事触发 Test Session |
| POST | `/admin/test-stories/run-batch` | 一次为 1..50 个故事各创建一个 Test Session |

判定：`EvaluateSessionWithFindings` 在原有"必测项 + 证据"之上增加一条——**未关闭的 blocker finding 不能 PASS**。其余严重级别只记录，供人工审阅。

## 7. 控制台

新增「运行中心 / 测试故事」页面：

- 项目维度故事库，按标题/slug 搜索；
- 导入多个 `.md`（按 `project + slug` 幂等覆盖）；
- 导入整个目录（浏览器目录选择，按相对路径推导标题/slug）；
- 新建/编辑：标题、slug、模式、环境、平台、超时、必测项、标签、Markdown 正文；
- 编辑/预览切换，预览使用 `react-markdown` + `rehype-sanitize`；
- 一键触发测试；多选故事后批量触发（每个故事独立快照，逐条返回成功/失败）；
- 最近测试列表：状态、判定、机器、创建时间，运行中自动刷新；
- 详情：执行事件时间线（claim/agent/evidence/check/finding/report）、
  测试方法（必测项 + 实际记录）、findings（严重级别/复现/预期/实际/证据）、输入包哈希、
  证据包（全部证据可预览/下载，图片直接浮层查看）；
- 执行事件只同步粗粒度步骤，Agent 的 JSONL 转录作为证据保存，避免事件表被刷爆。

### 7.1 事件与测试方法

故事可以声明一组可单独触发的“事件”，每个事件绑定一个测试方法（即 check id）：

| 字段 | 说明 |
|---|---|
| `id` | 事件标识，小写字母/数字/`-`/`_`，同一故事内唯一 |
| `title` | 事件名称 |
| `trigger_kind` | `ui` / `shell` / `http` / `manual` |
| `trigger` | 触发内容：UI 操作描述、shell 命令、HTTP 请求或人工步骤 |
| `method` | 测试方法 ID（`engine_check_record` 的 `id`），同一故事内唯一 |
| `expected` | 该事件的通过标准 |

- 存储：`test_stories.events`（migration 61，JSONB）；触发时快照进 `SessionSpec.events`，历史记录不受后续编辑影响；
- 导入：Markdown front matter 支持 `events:` 列表（`id/title/trigger_kind/trigger/method/expected`），未写 `required_checks` 时自动用事件方法补齐；
- 触发：`POST /admin/test-stories/{id}/run` 支持 `event_ids`；只选部分事件时本次必测项收敛为所选事件的方法，全选时并入故事其它必测项；
- 引擎：agent prompt 与 `engine_job` 都输出本次事件清单，agent 按事件执行并用 `engine_check_record` 记录对应 method；
- 控制台：故事列表显示事件数与方法，查看弹窗展示正文与事件表，触发弹窗可勾选事件，会话详情按 method 关联检查结果与证据。

## 8. 代码落点

| 层 | 文件 |
|---|---|
| 契约类型 | `backend/internal/testlab/session.go`、`backend/internal/testlab/story.go` |
| 存储 | `backend/internal/store/test_session_engine.go`、`test_session_events.go`、`test_story.go` |
| 管理 API | `backend/internal/adminapi/test_session.go`、`test_session_engine.go`、`test_session_events.go`、`test_stories.go` |
| 引擎 Supervisor | `test-runner/src/engine/host.ts` |
| 输入物化 | `test-runner/src/engine/inputs.ts` |
| 故事解析 | `test-runner/src/engine/stories.ts` |
| 热替换 | `test-runner/src/engine/hotswap.ts` |
| Agent 启动 | `test-runner/src/engine/agent.ts` |
| 本地 API | `test-runner/src/engine/server.ts` |
| 截图后端 | `test-runner/src/engine/screenshot.ts`、`test-runner/scenarios/windows/capture-window.ps1` |
| MCP | `test-runner/src/engine/mcp.ts` |
| 作业状态 | `test-runner/src/engine/job-state.ts` |
| 控制台 | `web/src/pages/TestStoriesPage.tsx` |

## 9. 安全边界

- 机器令牌只用于 `/test-runner/*`，不会进入 Agent 环境；Agent 只拿 job-scoped 引擎令牌。
- 引擎 HTTP 只监听 `127.0.0.1`；`/v1/jobs` 需要 control token。
- 输入包、故事、热替换目标都做路径约束；归档拒绝链接与路径穿越。
- 热替换默认 `restore_policy=always`；生产 `app.asar` 不允许替换。
- 专用测试用户 + 独立 `stateRoot` + 独立 Master 数据目录；高风险故事应在 VM 快照上执行。
- 发布门禁与故事型测试分离，避免"Agent 说 PASS 就发布"。

## 10. 验证记录（2026-11-18 真机）

环境：SmartPipe `192.168.197.109`（Docker，admin `:8092`），测试机 `192.168.197.108`（Windows 10，Syntropy 0.5.21）。

### 10.1 本地检查

| 检查 | 结果 |
|---|---|
| `go build ./...` | 通过 |
| `go test ./internal/store ./internal/testlab ./internal/adminapi` | 通过 |
| `test-runner npm test` | 47/47 通过（11-19 增至 50/50） |
| `web npm run build` | 通过（仅既有 chunk 体积告警） |

### 10.2 真机故事会话

故事 `story_6123ccef`（Syntropy 安装冒烟，必测方法 `install-layout`）连续触发多次，最终链路结果：

| 会话 | 结果 | 说明 |
|---|---|---|
| `session-074b07943a17ace1453f3261a63a6965` | CANCELLED（人工取消） | 首轮暴露证据未上报，见 10.3 |
| `session-d7d8536321dc74aa8b8a8040408de7ce` | BLOCKED（租约回收） | 同上，runner 被回收后自动置 BLOCKED |
| `session-2a4223d35e7061bf7783233668caf255` | PASS | 证据 + 检查 + 报告闭环 |
| `session-a0b5d8eb4abec5a2e103215ebaa1ec46` | PASS | 执行事件时间线开始落库 |
| `session-5776827a6e379a012d31b1b01721028c` | PASS | 含 1 条 info finding + 2 份证据；报告事件已同步 |

`session-5776827a6e379a012d31b1b01721028c` 的关键事实：

- 检查 `install-layout=PASS`，证据 `sha256:46777e9e…`（1050 字节的 `--help` 输出）；
- finding `help-flag-spelling`（info/open）：帮助里是 Go 单横线 `-server-addr`，故事写的是 `--server-addr`；info 不阻断 PASS；
- 事件表 13 条（过滤了 agent JSONL）：`claimed → agent → evidence → check → finding → finish → completed → warning → evidence → report → report`；
- 控制台「测试故事 → 详情」可看到执行事件、测试方法、问题列表、证据下载与输入包；
- 报告后机器租约被释放（`session_id` 清空）。

### 10.3 本轮修复的真实缺陷

| 缺陷 | 根因 | 修复 |
|---|---|---|
| runner 启动即挂 | Windows 写出的 `session.json` 带 BOM | `dispatch-cli` / `session-host-cli` 读取时去除 BOM |
| 引擎启动失败后进程不退 | `engine-jobs` 目录未建 → 写 runtime 文件 ENOENT，监听句柄未关 | 启动前 `mkdir`，失败时关闭 server |
| 会话结束后 runner 卡住 | `server.close()` 等待 MCP/Agent 残留 keep-alive 连接 | `closeAllConnections()` + 5s 兜底 + `dispatch-cli` 关闭超时 |
| 报告 400：`check … references unknown evidence` | Agent 证据只存在引擎本地，从未上传 SmartPipe | `sync()` 先上传未发送证据，再同步检查项/问题 |
| 取消会话后机器永久 busy | `CancelTestSession` 未清理 `desktop_test_machines` 租约 | 取消时同步释放机器 |
| runner 崩溃后 RUNNING 会话永久占用机器 | 领取只回收终态会话 | 租约过期且状态 RUNNING → 置 BLOCKED 并回收 |
| release run 忙时会话永远不被领取 | `dispatch-cli` 遇 run 409 直接 `exit(0)` | 改为继续尝试会话领取 |
| 详情面板白屏 | 输入包为空时接口返回 `null`，前端 `.map` 抛错 | 前端 `?? []`，后端返回 `[]` |
| 状态文件被 PowerShell 5.1 拒绝 | 事件消息含控制字符 | 写入前清洗 C0 控制符 |
| 截图告警乱码 | PowerShell 5.1 用 GBK 输出本地化异常 | 固定 ASCII 提示 + 明确“会话需保持连接解锁” |

### 10.4 已知限制（截至 2026-11-18）

- **Electron 截图依赖调试端口**：断开的 RDP 会话里 Chromium 不再绘制，`PrintWindow` 只能拿到灰底。11-19 已用 CDP 解决（见 11.1），前提是被测应用以 `--remote-debugging-port` 启动。
- **智能体 MCP 懒加载**：本机 Codex 1.1.0 把 `smartpipe_test` MCP 标为 deferred，Agent 通过认证的 loopback HTTP 完成同样的 check/finding/evidence/finish；MCP 路径仍需在支持工具直连的版本上复验。
- **`app.asar` 热替换被完整性校验拒绝**：单测覆盖拒绝逻辑，真机未尝试（替换会触发 Electron asar integrity 失败）。
- **输入包上限**：单包 256 MiB、单会话证据 64 MiB；Syntropy 完整 `resources` 1.79 GiB 不能整包上传，隔离副本目前用 `robocopy` 一次性准备。
- **隔离副本只覆盖 `resources`**：`C:\SmartPipeTest\syntropy-isolated` 未包含 `Syntropy.exe` 等安装根其余文件，完整隔离启动验收仍未做。

## 11. 2026-11-19 补充验证

### 11.1 CDP 截图（Electron / 断开 RDP）

现象：断开的 RDP 会话里 `capture-window.ps1 -Title Syntropy` 返回 0，但图片是 7.4 KB 的纯灰面；`CopyFromScreen` 失败后回退 `PrintWindow`，Chromium 在没有渲染桌面时不出新帧。

方案：被测 Electron 应用以 `--remote-debugging-port=<port>` 启动；引擎优先通过 DevTools 协议 `Page.captureScreenshot` 取一帧，失败再退回窗口标题 / 全屏。

落地：

- `test-runner/src/engine/screenshot.ts`：`captureScreenshot` 顺序 CDP → 标题窗口 → 全屏；`pickPageTarget` 优先 `app://` 渲染页，避开启动闪屏。
- `EngineHostConfig.screenshotCDPPort` / `screenshotCDPURLMatch` / `screenshotCDPProcess`；`session.json` 同名配置。
- `/v1/jobs/{id}/capture` 同样优先 CDP，并在响应里返回 `via`。
- 运维：`run-worker.ps1` 读取 `worker.json` 的 `desktopApp`，登录后进程不在时带调试参数拉起；`deploy-runner.ps1` 新增 `-DesktopDebugPort`、`-ScreenshotTitle`、`-UseEngine`、`-EnginePort`、`-CodexCommand`。
- 端口发现：Electron 42 不写 `DevToolsActivePort`，`--remote-debugging-port=0` 会随机监听。引擎在首选端口失败时，用 `tasklist` + `netstat` 找出该进程的监听端口，逐个探测 `/json/version`，命中 CDP 后再截图；因此进程被强杀留下的僵尸端口不会拖死后续测试。

验证结果：

| 项 | 结果 |
|---|---|
| 直接 CDP 抓 `app://renderer/index.html` | 1456×908 真实 Syntropy 主界面 |
| 故事会话 `session-eeb1af33292affdf4e9478c23c25d62a` | 事件 `uploaded ... final-screen.png via cdp`，76 760 B |
| 会话内 agent 截图（`engine_evidence_capture`） | 2 张 PNG（76 760 / 64 486 B） |
| 停掉 Syntropy 后重启计划任务 | worker 自动带 `--remote-debugging-port=9334` 拉起，CDP 可用 |

注意：Windows 上被强杀的进程可能残留僵尸监听端口（本次 9333 被已退出 PID 占住，新进程无法绑定），换端口或重启机器即可；部署时不要复用被占端口。

### 11.2 普通文件输入与 blocker 降级

会话 `session-434f6b8285d021302546f8973382dad3`，结果 `FAIL`，同时验证两件事：

- 单文件输入 `kind=file`，62 B，sha256 `02966d0f…`；引擎下载、校验并落盘到 `inputs/notes/notes`。
- Agent 读到 `SMART PIPE FILE INPUT OK`，上传为证据，记录 `file-input-loaded=PASS`。
- Agent 按故事上报 `severity=blocker` 的 finding（规则自检），再请求 PASS；平台 `EvaluateSessionWithFindings` 将最终判定降为 `FAIL`。

观察点：`file` 输入默认落盘名就是输入名（本次 `notes`）；故事若要求 `notes.txt`，调度侧应显式给 `target`。本次 Agent 自动发现并纠正了文件名差异。

### 11.3 控制台证据包与预览

- 新增 `GET /admin/test-sessions/{id}/evidence`，只返回 `sha256`/`mime_type`/`bytes`，载荷仍走单摘要下载。
- 详情页新增「证据包」：全部证据可预览或下载；图片通过带 Bearer 的 `fetch` 转 object URL，在浮层中直接查看截图。
- 检查项说明列限宽 520×180 并可滚动，避免长 note 把表格撑爆。

截图：`.dev/shots/session-detail.png`、`.dev/shots/evidence-preview.png`。

### 11.4 故事事件与测试方法

- 模型：`StoryEvent{id,title,trigger_kind,trigger,method,expected}`；方法 ID 即 `engine_check_record` 的检查项 ID，故事内唯一；
- 存储：migration 61 增加 `test_stories.events`，触发时快照进 `SessionSpec.events`；
- 导入：Markdown front matter 的 `events:` 列表可直接落库，未写 `required_checks` 时自动用事件方法补齐；
- 触发：`POST /admin/test-stories/{id}/run` 支持 `event_ids`；只选部分事件时，必测项收敛为所选方法，并给故事正文追加“本次运行范围”说明；
- 引擎：agent prompt 与 `engine_job` 都带事件清单，agent 按 method 逐条回填检查项；
- 控制台：故事列表显示事件数；查看弹窗展示正文与事件表；编辑弹窗可增删改事件；触发弹窗可勾选事件；会话详情按 method 关联事件、检查结果与证据。

真机结果：

| 会话 | 范围 | 结果 |
|---|---|---|
| `session-590e8b3bb02aa15c0f6aec4c249bfe9e` | 只选 `manifest-present` | PASS，`install-layout` 一条检查通过 |
| `session-d479a1a1589b6e65e3e928767d018c39` | 导入故事后只选一个事件 | PASS，只执行所选事件（无额外 `--help`） |
| `session-a0167415c7eb4f23920b28cbf1460ef8` | 全选两个事件 | PASS，`install-layout` 与 `app-server-help` 均为 PASS |

截图：`.dev/shots/stories-list.png`、`.dev/shots/story-view.png`、`.dev/shots/story-run-dialog.png`、`.dev/shots/story-editor-events.png`、`.dev/shots/session-events-methods.png`。

### 11.5 真实安装目录热替换与故障恢复

在 `%LOCALAPPDATA%\Programs\Syntropy` 上，用引擎计划替换
`resources/plugins/quant/ui/styles.css`，任务结束自动还原：

| 会话 | 场景 | 结果 |
|---|---|---|
| `session-c24a3ff2d69cf76845db20d2cae9df90` | 停止应用 → 替换插件样式 → 重启 → agent 验证 + CDP 截图 | PASS；`plugin_file_swapped`、`app_window_after_swap` 均 PASS |
| `session-99149ed4dd732e4f61a6ca58130e44fe` | 替换后 `exec exit 1` 故意失败 | BLOCKED；文件已还原，`restore_steps` 重新拉起 Syntropy |
