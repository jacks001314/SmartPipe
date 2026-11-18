# Syntropy 故事化测试系统：现状分析与落地记录

日期：2026-11-18

## 结论

原有的在线测试系统只解决“发布制品是否满足固定门禁”：输入是源码提交、制品哈希、场景版本和必测项，输出是 PASS/FAIL/BLOCKED。它不能表达“修了一个 bug，去真机上按这个故事复现并验证”，也没有给测试 Agent 任何可调用的执行接口。

本轮在此基础上新增了 **Test Session（会话型测试）**：

- 会话由故事（Markdown）驱动，Agent 通过 MCP 工具在真实 Windows 桌面上执行命令、读写文件、发 HTTP、启动/停止进程、截图，并逐条记录带证据的检查。
- Runner 仍然是机器的唯一所有者：持有桌面锁、执行受限命令词表、上传证据、清理并释放机器。
- 后端保存会话、命令流水、检查项和证据，并按服务端规则计算最终判定。Agent 声明的 PASS 不能绕过必测项和证据要求。
- 会话型测试与发布门禁共存：同一个常驻 Runner 先领取确定性测试运行，再领取会话。

已在 `192.168.197.108` 的真实 Syntropy 安装上跑通一条完整会话（PASS，三项检查均有证据，截图已人工确认）。

## 一、原有系统实现情况

### 1.1 组件

| 组件 | 位置 | 职责 |
|---|---|---|
| 测试任务/机器/租约/证据存储 | `backend/internal/store/testlab*.go` | 任务状态机、机器令牌、代次隔离、证据配额 |
| 判定规则 | `backend/internal/testlab/run.go` | 输入身份校验、必测项与证据校验、发布门禁匹配 |
| 管理 API | `backend/internal/adminapi/testlab*.go` | 建任务、启停、取消、恢复、证据下载 |
| Runner | `test-runner/src/worker.ts` | 领取/续租/执行/上传/报告/释放 |
| 场景 | `test-runner/src/{bundle,command,observe}-scenario.ts` | 热替换、固定命令、桌面观察 |
| Windows 适配 | `test-runner/scenarios/syntropy/windows/*.ps1` | 安装目录定位、启停、窗口等待、截图 |

### 1.2 已有约束（设计上是有意的）

- 任务输入不可变：`source_sha`、`artifact_sha256`、`scenario@version`、`platform`、`required_checks`。
- 服务端只选能力，不下发任意命令；命令场景的脚本和哈希在机器上预置。
- 机器令牌只用于 `/test-runner/*`，Runner 子进程不继承令牌。
- 60 秒租约、15 秒续租、20 分钟总期限；失败或强杀后机器保持隔离，需管理员确认清理后恢复。
- 证据按 SHA-256 内容寻址，单次运行总量受限；报告中的证据必须在服务端可查。
- 门禁不接受 Runner 或调用方自报的 PASS，必须按当前策略重新计算。

### 1.3 已验证

- 热替换任务 PASS，六项检查齐全；错误制品哈希自动 BLOCKED 并释放；过期锁可被接管。
- `npm test` 35 项通过；Go `internal/testlab`、`internal/store`（会话相关）、`internal/adminapi` 通过。

### 1.4 缺口

- 只能测“制品 + 固定场景”，无法测“某次修复 / 某组故事”。
- 没有 Agent 可调用的观察、操作、断言接口。
- 没有自由文本故事的载体、执行记录和证据关联。
- 没有“Agent 结论 + 服务端必测项”两层判定。

## 二、Test Session 模型

### 2.1 状态机

```
QUEUED -> RUNNING -> PASS | FAIL | BLOCKED | CANCELLED
```

- `QUEUED`：等待匹配平台/环境的 Runner。
- `RUNNING`：Runner 持有机器会话租约；Agent 可下发命令、记录检查、上传证据。
- 终态由 Runner 在清理后提交，服务端重新计算，不直接采用 Agent 的字符串。

### 2.2 会话规格

```json
{
  "project_id": "prj_0116552a",
  "title": "Syntropy 安装完好性与主界面冒烟",
  "kind": "story",
  "environment": "syntropy",
  "platform": "windows-amd64",
  "story_ref": "test-runner/scenarios/syntropy/stories/install-smoke.md",
  "story": "# Markdown ...",
  "inputs": {"install_root": "C:\\Users\\windows\\AppData\\Local\\Programs\\Syntropy"},
  "required_checks": ["app_installed", "app_window", "screenshot"],
  "timeout_seconds": 900,
  "work_dir": "C:\\SmartPipeTest\\work"
}
```

- `kind`：`story`（功能故事）、`bug`（缺陷复现/修复验证）、`explore`（探索性）。
- `story` 是注入给 Agent 的全文；`story_ref` 只做来源追踪。
- `required_checks` 为空时，PASS 仍要求至少一条带证据的 PASS 检查。
- 故事可以是任意 Markdown，不需要改 Runner 代码；执行自由度由命令词表和安全边界约束。

### 2.3 判定规则

`EvaluateSession` 的优先级：

1. 必测项任一 `FAIL` -> `FAIL`。
2. 必测项缺失、非 PASS 或无证据 -> `BLOCKED`。
3. Agent 明确 `FAIL` / `BLOCKED` -> 采用。
4. 否则所有必测项 PASS 且有证据 -> `PASS`。
5. 无必测项时，至少一条 `PASS` 检查带证据才算 `PASS`。

## 三、接口

### 3.1 Agent 侧（MCP，stdio）

启动方式：

```
SMARTPIPE_API_URL=http://127.0.0.1:18092 \
SMARTPIPE_TEST_SESSION_ID=session-... \
SMARTPIPE_TEST_SESSION_TOKEN=... \
node test-runner/dist/src/test-mcp.js
```

| 工具 | 作用 |
|---|---|
| `test_session` | 会话元数据、输入、故事全文、已有检查 |
| `story_read` | 只读故事 |
| `env_exec` / `env_shell` | 真机执行可执行文件 / PowerShell 脚本 |
| `env_read_file` / `env_write_file` / `env_list` | 受根目录约束的文件操作 |
| `env_http` | 从测试机访问本地或远端 HTTP |
| `env_processes` / `env_start` / `env_stop` | 进程观察与控制 |
| `ui_screenshot` | 按窗口标题或全屏截图，返回 PNG 与证据引用 |
| `upload_file` | 把真机文件上传为证据 |
| `check_record` | 记录/覆盖一条检查（PASS 必须有证据） |
| `evidence_upload` | 上传内联证据（≤8 MiB） |
| `test_finish` | 请求 Runner 清理并结束会话 |

### 3.2 Runner 侧（机器令牌）

`POST /test-runner/session/{claim,heartbeat,next,result,evidence,report,release}`

- `next` 是 25 秒长轮询；会话终态返回 409，Runner 停止轮询并释放。
- 命令结果和证据都与 `session_id + generation` 绑定，旧代次不能写入。

### 3.3 管理侧

`GET/POST /admin/test-sessions`、`/admin/test-sessions/batch`（一次最多 50 条故事）、`/{id}/cancel`、`/{id}/commands`、`/{id}/evidence/{sha256}`。

创建时只返回一次会话令牌，数据库只存摘要。

### 3.4 命令词表

`exec`、`shell`、`read_file`、`write_file`、`list`、`http`、`processes`、`start_process`、`stop_process`、`screenshot`、`upload_file`、`sleep`、`finish`。

Runner 侧再次约束：

- 文件和 cwd 必须位于 `allowedRoots`。
- 可执行文件受 `execAllowlist` 约束（`taskkill` 等 Runner 自身工具除外）。
- 单命令超时 ≤ 600 秒；stdout/stderr 截断 128 KiB；文件 ≤ 8 MiB。
- 子进程只继承白名单环境变量，不继承机器令牌。

## 四、与 Syntropy 的结合

### 4.1 三条执行路径

| 路径 | 适用 | 状态 |
|---|---|---|
| 真实安装版 + 桌面/进程/文件/截图 | 升级、热替换、UI 冒烟、真实依赖 | 已跑通 |
| `syntropy-app-server.exe` HTTP API | 业务逻辑、项目/会话/Agent、无需界面 | 已手工探测，待接入会话适配器 |
| Playwright Electron | 前端交互细节 | 需要 E2E 构建，安装版 `app.asar` 有完整性校验，暂不作为主路径 |

### 4.2 本轮实测

会话 `session-6feb1f98af9979a27703ab4f68c89db8`：

- `app_installed` PASS，证据 `sha256:4404b6f1...`
- `app_window` PASS，证据 `sha256:5180d536...`
- `screenshot` PASS，证据 `sha256:5c4f17e1...`
- 服务端判定 `PASS`，机器自动释放，下一轮即可领取新会话。

截图经人工确认是真实 Syntropy 主界面。同时发现：主界面提示本地后端 `127.0.0.1:3333` 连接被拒绝，说明“窗口出现”不等于“产品可用”。这正说明故事必须包含业务检查项，而不是只做窗口冒烟。

随后用真实 Codex Agent（而非脚本）执行了只读诊断故事
`session-3b7dbc8def82fb53f4d2b895843351bb`：

- Agent 通过 MCP 调用了 `test_session`、`env_processes`、`env_shell`、`env_http`、`ui_screenshot`、`check_record`、`test_finish`。
- `backend_port` 记录 FAIL：3333 端口由 `mbot.exe`（PID 11828）监听，但两次 HTTP GET 均 `fetch failed`，原始 HTTP/1.1 请求收到 0 字节后连接被关闭。
- `backend_state` 记录 PASS：截图证据正常，但明确注明“界面可见不等于后端可用”。
- 会话最终 `FAIL`，摘要包含可复现步骤，机器自动释放。

这验证了“用户给故事、Agent 自主观察与断言、平台强制证据与服务端判定”的闭环；同时也暴露出安装版当前本地后端不可用（待 app-server 隔离与 local master 部署解决）。

### 4.3 稳定性修复

- 后端重启导致 Runner 长轮询 `fetch failed` 时，会话被误判 BLOCKED。现已对 `next`/`heartbeat`/`result`/`report`/`release` 做有限重试；HTTP 409 视为终态不重试。
- 会话终态但 Runner 已离线的机器，在租约过期后可由下一次 `claim` 自动回收，避免机器被永久占住。
- 常驻 Runner 合并为 `dispatch-cli`：先领取发布运行，再领取会话，两者共用同一桌面锁。

## 五、仍然缺失

1. **Syntropy app-server 适配器**：用独立 `SYNTROPY_HOME` 启动、local master 部署、端口回收；当前安装版的默认 `~/.syntropy` 槽位被真实桌面占用，必须隔离。
2. **UI 动作工具**：现在只有截图；点击/输入/滚动应复用 CUA SDK，并保持串行与不重放语义。
3. **Agent 自动拉起**：当前由操作员或外部 Codex 连接 MCP；后端可用 `codexrunner` 复用发布 Agent 的受限 MCP 模式自动执行。
4. **故事库与批量编排**：已有批量创建接口，但缺少按目录扫描、优先级、依赖和汇总报告。
5. **会话超时回收**：超过 deadline 后 Runner 会提交 BLOCKED；若 Runner 本身死亡，仍需管理员取消/回收（租约过期后可自动 reclaim）。
6. **确定性门禁与会话的联动**：会话结论目前不参与发布门禁，避免“Agent 自评”直接放行。

## 六、复现命令

```powershell
# 1. 构建
cd test-runner; npm test

# 2. 创建会话（管理令牌）
curl -X POST -H "Authorization: Bearer $env:SMARTPIPE_ADMIN_TOKEN" `
  -H "Content-Type: application/json" --data-binary "@session-create.json" `
  http://127.0.0.1:8092/admin/test-sessions

# 3. 无模型冒烟（验证 MCP + Runner + 真机）
$env:SMARTPIPE_API_URL="http://127.0.0.1:18092"
$env:SMARTPIPE_TEST_SESSION_ID="session-..."
$env:SMARTPIPE_TEST_SESSION_TOKEN="..."
node test-runner/examples/session-smoke.mjs
```

Windows 常驻任务：`SmartPipeTestRunner` 运行 `run-worker.ps1 -Loop -IntervalSec 5`；`worker.json` 含 `sessionConfig` 时自动切换到 `dispatch-cli.js`。
