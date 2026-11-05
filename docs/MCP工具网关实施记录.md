# MCP 工具网关实施记录

更新时间：2026-11-05

## 1. 解决什么问题

Agent（lead / dev / test / release）需要调用补丁验证、派生、发版、Issue 评论等能力，但不应持有 GitHub 或蓝信凭据，也不应使用管理员令牌直接访问 `/admin`。

`smartpipe-mcp` 是唯一的工具入口：

```
Agent ──Bearer spt_…──▶ smartpipe-mcp(:8097/mcp)
                           ├─ 校验令牌（admin /admin/agent-tokens/verify）
                           ├─ 按角色裁剪工具集
                           ├─ 项目绑定：令牌只能操作自己的 project_id
                           └─ 用服务端管理员令牌调用 /admin/*
                                  ├─ verifications（patch-verifier）
                                  ├─ derivations（App Server /api/agents）
                                  ├─ releases/runs（构建、tag、Release、通知）
                                  └─ issues/*（GitHub 评论、标签）
```

## 2. 令牌模型

表 `agent_tokens`（迁移 v13）：

| 字段 | 说明 |
|---|---|
| `id` | `tok_…` |
| `project_id` | 绑定项目；为空表示可跨项目（管理用途） |
| `role` | `lead` / `dev` / `test` / `release` |
| `name` | 便于识别的名称 |
| `prefix` | 前 12 位，用于界面展示 |
| `token_hash` | sha256(明文)，明文只在签发响应中出现一次 |
| `enabled` | 撤销后立即失效 |
| `last_used_at` | 最近一次校验通过时间 |

管理接口：

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/admin/agent-tokens` | 签发，返回 `{token, record}` |
| GET | `/admin/agent-tokens?project_id=` | 列表（不含明文/哈希） |
| POST | `/admin/agent-tokens/{id}/revoke` | 撤销 |
| POST | `/admin/agent-tokens/verify` | 网关内部校验，刷新 `last_used_at` |

## 3. 工具与角色

| 工具 | lead | dev | test | release | 说明 |
|---|---|---|---|---|---|
| `project.info` | ✓ | ✓ | ✓ | ✓ | 项目配置（无凭据） |
| `issue.get` | ✓ | | | | 读取 Issue |
| `issue.comment` | ✓ | | | | GitHub 评论 |
| `issue.add_labels` | ✓ | | | | 追加标签（如 ready） |
| `release.candidates` | ✓ | | | ✓ | ready 候选 Issue |
| `verify.patch` | ✓ | | | | 干净副本验证（含审批门禁） |
| `repo.commit` | ✓ | | | | 建分支提交，可合并目标分支（含审批门禁） |
| `repo.push` | ✓ | | | | 推送分支，远端与凭据由服务端注入，不 force |
| `derivation.create` | ✓ | | | | 派生子 Agent（含审批门禁） |
| `release.run` | ✓ | | | ✓ | 构建并发布（支持 dry_run） |
| `release.rollback` | ✓ | | | ✓ | 回滚版本 |

子 Agent 默认没有 GitHub/蓝信通信工具，只能读项目信息或向 lead 回传证据。

## 4. 传输

**HTTP**：`POST /mcp`，`Authorization: Bearer spt_…`，请求体为单个 JSON-RPC 2.0 消息，响应为 JSON。

**stdio**：`smartpipe-mcp.exe -stdio`，按行读写 JSON-RPC；令牌来自 `SMARTPIPE_MCP_TOKEN`，管理员令牌来自 `SMARTPIPE_ADMIN_TOKEN`。

支持方法：`initialize`、`notifications/*`（202）、`ping`、`tools/list`、`tools/call`。
每个令牌默认 120 次/分钟；超过返回 JSON-RPC `-32000 rate limit exceeded`。

错误处理约定：工具被拒绝、参数错误、后端返回 `approval_required` 等都以 `isError: true` 的文本返回，便于 Agent 读到原因；协议级错误才用 JSON-RPC `error`。

## 5. 端到端实测（2026-11-05）

```
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\mcp-smoke.ps1
MCP SMOKE OK project=prj_5148b505 lead_token=tok_d179de30a7ea
  tools=project.info,issue.get,issue.comment,issue.add_labels,release.candidates,verify.patch,derivation.create,release.run,release.rollback
  verify_report_ok=True patch_hash=sha256:982aa1de72b4ea2a712251298aa504d6ec21794c78f8a71dc0602e1adb77fe48
  dev_tools=project.info revoked_status=401
```

覆盖：

1. 管理员签发 lead 令牌（`spt_…`），`initialize` 返回 `protocolVersion=2025-06-18`。
2. lead 看到 9 个工具；`project.info` 返回 `release-demo`。
3. `issue.comment` 在假 GitHub 的 #101 留下评论。
4. `verify.patch` 在 `.dev/sandbox-repo` 上跑完 build+unit，`ok=true`，`patch_hash` 与 M2 记录一致。
5. dev 令牌只看到 `project.info`，调用 `derivation.create` 返回 `isError`。
6. 撤销 lead 令牌后再次调用返回 HTTP 401。
7. stdio 模式（`-stdio` + `SMARTPIPE_MCP_TOKEN`）同样返回 `initialize` 与裁剪后的 `tools/list`。

管理台「Agent 令牌」页：签发、列表（前缀/角色/最近使用/状态）、撤销、一次性明文弹窗。截图 `docs/images/console-tokens.png`。

## 6. 测试

```powershell
cd backend
go test ./internal/mcp/ -count=1
```

覆盖：initialize 与协议版本、按角色裁剪工具、越权调用不触达 executor、执行错误转为 `isError`、令牌限流、HTTP 401/202/200、AdminBackend 路径与参数映射（评论/标签/发版）、项目越界拒绝、错误信封透传。

## 7. 环境变量

| 变量 | 说明 |
|---|---|
| `SMARTPIPE_MCP_ADDR` | 监听地址，默认 `:8097` |
| `SMARTPIPE_ADMIN_URL` | 管理接口地址，默认 `http://127.0.0.1:8092` |
| `SMARTPIPE_ADMIN_TOKEN` | 管理接口令牌（仅网关进程持有） |
| `SMARTPIPE_MCP_TOKEN` | stdio 模式下的 Agent 令牌 |

## 8. 接入 Syntropy Agent 运行环境

Syntropy 的 Agent 模板只能按名称引用全局 MCP Pool（`SystemSettings.mcp_pool`），Pool 里的 HTTP Server 支持
`bearer_token_env_var`：连接时用 Agent 进程环境里的变量作为 Bearer Token。利用这一点实现「一个网关、每 Agent 一个令牌」：

1. **全局 Pool**：`smartpipe = { http: { url: http://127.0.0.1:8097/mcp, bearerTokenEnvVar: SMARTPIPE_MCP_TOKEN } }`。
2. **模板**：`pipeline-lead-v3` 的 `mcpConfig.servers = ["smartpipe"]`（导入脚本自动带上）。
3. **部署 Agent**：`POST /admin/runtime/app-servers/{id}/agents` 带 `mcp_role=lead|dev|test|release` 时，
   管理台先为该 Agent 签发 `spt_…` 令牌，把 `SMARTPIPE_MCP_TOKEN` 注入 `spec.environment`，
   并在 `labels` 记录 `smartpipe.mcp_role` 与 `smartpipe.token_id`。
4. **清理**：删除 Agent 后用记录里的 `token_id` 撤销令牌。

新增管理接口：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/admin/runtime/app-servers/{id}/mcp` | 读取 Pool（隐藏 header 值） |
| POST | `/admin/runtime/app-servers/{id}/mcp/servers` | 写入/更新 HTTP MCP Server（GET+合并+PUT，保留其它条目） |
| DELETE | `/admin/runtime/app-servers/{id}/mcp/servers/{name}` | 移除 |
| GET | `/admin/runtime/app-servers/{id}/mcp/statuses` | Agent 上 MCP 鉴权/工具数量 |
| GET/PUT | `/admin/runtime/app-servers/{id}/agent-templates/{name}` | 读取/更新模板（导入脚本据此改配置） |
| DELETE | `/admin/runtime/app-servers/{id}/agents/{agent}` | 删除 Agent |

实测（`scripts/mcp-agent-smoke.ps1`）：

```
app_server=appsrv-86s6w
pool=smartpipe http http://127.0.0.1:8097/mcp token_env=SMARTPIPE_MCP_TOKEN
template.mcp_config.servers=smartpipe
node=d835265b8ff7530dc874dfa4d40a897e
agent=agent-13795ad6c110c7d6a62d5dc9 state=4
agent env SMARTPIPE_MCP_TOKEN=spt_b2a96df9… role=lead
injected token -> mcp tools=9 (verify.patch/derivation.create/release.run present)
agent deleted, token revoked: tok_6ea6a6bffdfe
MCP AGENT SMOKE OK app_server=appsrv-86s6w agent=mcp-lead-6d1a7c tools=9
```

说明：Agent 状态为 `phase=4`（RUNNING），但沙箱里的 Codex 进程没有模型会话，`/mcp/statuses` 返回
`agent is killed`、`toolCount=0`；因此这里用「注入给 Agent 的同一个令牌」直接调用网关，验证了 9 个工具可见。
真实环境里 Agent 建立会话后，`/mcp/statuses` 应能看到 `serverName=smartpipe` 与工具数量。

## 9. 工具调用审计与配置可编辑

网关过去只把调用写在进程日志里，排查「哪个 Agent 调了什么」要去翻日志。这一节把调用记录落到数据库，并在管理台加了「工具调用」页。

### 9.1 记录链路

```
Agent --(spt_ 令牌)--> mcp-gateway --(异步 POST /admin/tool-calls)--> admin-api --> PostgreSQL tool_calls
```

- `mcp.Server` 增加 `Recorder` 接口，`tools/call` 的 `ok / denied / invalid_args / rate_limited / error` 五个分支都会回调，带上令牌、Agent、角色、工具名、耗时和错误。
- 网关侧 `mcpRecorder` 用 256 容量的有界队列异步回写，队列满时丢弃并打日志；管理接口失败不影响工具执行结果。
- 参数只保留可审计字段：`issue_ref / number / version / run_id / dry_run / agent_name / template_name / author`，附加 `keys`、`labels_count`、`patch_bytes`、`body_bytes`。**补丁全文与评论正文不落库**，只记字节数。

迁移 `v14 tool_calls_settings`：

```sql
CREATE TABLE tool_calls (
  id BIGSERIAL PRIMARY KEY, project_id TEXT, token_id TEXT NOT NULL,
  agent_name TEXT, role TEXT, tool TEXT NOT NULL, status TEXT NOT NULL,
  duration_ms INT, args_summary JSONB, error TEXT, created_at TIMESTAMPTZ DEFAULT now()
);
CREATE TABLE runtime_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TIMESTAMPTZ);
```

### 9.2 MCP 地址可在界面改

`GET|PUT /admin/runtime/mcp-config` 读写 `runtime_settings` 的 `mcp_url` / `mcp_pool_name`；
admin-api 启动时 `ApplyRuntimeSettings` 用库里的值覆盖环境变量，因此改了地址重启 admin-api 即生效，
Agent 下次部署会拿到新的 Pool 地址。界面改动会写审计 `runtime.mcp.config`。

### 9.3 新增接口

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/admin/tool-calls?tool=&limit=` | 最近调用，按工具过滤 |
| POST | `/admin/tool-calls` | 网关回写入口（管理员令牌） |
| GET | `/admin/runtime/mcp-config` | 读取 MCP 地址与 Pool 名 |
| PUT | `/admin/runtime/mcp-config` | 保存（校验 http(s)） |

### 9.4 实测

`scripts/mcp-smoke.ps1` 跑完后查询接口（真实输出）：

```
[{"id":5,"agent_name":"smoke-dev","role":"dev","tool":"derivation.create","status":"denied",
  "error":"工具 derivation.create 不在角色 dev 的权限范围内"},
 {"id":4,"agent_name":"smoke-lead","role":"lead","tool":"verify.patch","status":"ok","duration_ms":3349,
  "args_summary":{"issue_ref":"example/release#101","patch_bytes":546,...}},
 {"id":3,"tool":"issue.comment","status":"ok","args_summary":{"number":101,"body_bytes":24}}]
```

可以看到 `verify.patch` 只留下 `patch_bytes=546`、`issue.comment` 只留下 `body_bytes=24`，正文没有入库。

界面截图：

![工具调用审计](images/console-toolcalls.png)

单元测试 `internal/mcp/recorder_test.go` 覆盖：denied/ok/error 都会记录、principal 信息透传、
`SummarizeArgs` 丢弃 `patch`/`body` 并保留字节数。

## 10. 仍待完成

1. **OAuth / 动态令牌**：目前是静态令牌 + 撤销；多租户场景需要短期令牌与刷新。
2. **更细的权限**：按标签/Issue 限制工具范围（例如 release 令牌只能操作指定项目版本）。
3. **Streamable HTTP（SSE）**：当前实现单请求 JSON 响应，未实现服务端推送。
4. **会话级验证**：沙箱缺少模型凭据，无法驱动真实 Codex 会话加载 MCP；需要在有模型的环境里再跑一次 `/mcp/statuses` 断言。