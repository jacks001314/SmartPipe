# Codex 执行器实施记录

日期：2026-11-05  
范围：`backend/internal/codexrunner`、`backend/cmd/codex-runner`  
决策：先接 Codex，成熟后再接 Syntropy 的 Agent 团队。

## 1. 为什么先接 Codex

Syntropy 的插件不能驱动 Agent——桌面侧 `pluginMethodAllowed` 对 `agent.dispatch`、
`agent.sendMessage`、`agent.retry` 直接返回 `false`，宿主侧 `experience-host-call-broker`
也把同样四个方法列进黑名单。要走 Syntropy 就得改成“Agent 调插件工具”的拉取式流程，
或者改 Syntropy 自己的 Go 代码加原生动态工具。

Codex 没有这个约束：`codex app-server` 就是一个本地进程，能用 SDK 起、能跑 turn。
所以先把执行器做出来，管道里“谁写代码”这一环就能独立验证，之后再决定是否换成
Syntropy 的 Agent。

## 2. 用 codex-sdk-go 而不是拼命令行

依赖：`github.com/pmenglund/codex-sdk-go v0.166.0`，与本机 `codex-cli 0.166.0` 版本对齐。
SDK 走 app-server 的 JSON-RPC，能拿到 `turn.Status`、`turn.TurnID`、`thread.ID()`、
`turn.TokenUsage`，命令行参数变化也不会把执行器弄坏。

注意：这个 SDK 的 `go.mod` 要求 `go >= 1.26.10`，所以 `backend/go.mod` 从 `1.25`
升到 `1.26.10`，`Dockerfile` 的两个 Go 阶段也跟着从 `golang:1.25-alpine` 改成
`golang:1.26-alpine`。

## 3. 组成了什么

`codexrunner.Run(ctx, Spec)` 负责一轮完整会话：

1. 用 `codex.New` 起 app-server（`SpawnOptions.CodexPath` 可指向指定二进制）；
2. `StartThread` 把工作目录设为待改的仓库，批准策略 `never`，沙箱 `workspace-write`，
   线程标记为 `ephemeral`；
3. `thread.Run` 跑完整个 turn，返回状态与最终回复；
4. 超时默认 30 分钟，可覆盖。

`codexrunner.Diff(ctx, workDir, gitBin)` 负责产出补丁：先 `git add -A`（按 `.gitignore`
过滤），再 `git diff --cached` 输出 unified diff。工作区没改动时返回空字符串。

`cmd/codex-runner` 是命令行入口：

```bash
codex-runner -work-dir /srv/ws/issue-42/repo -prompt-file /tmp/task.md \
  -out /tmp/42.patch -verify-repo https://github.com/owner/repo -base <commit> -issue owner/repo#42
```

带上 `-verify-repo` 时，补丁会立刻交给 `internal/patch` 的 `Verifier`，在干净副本上
应用并执行 `smartpipe.yaml` 的必过命令，结果写进 JSON 摘要。摘要字段包括
`status`、`turn_id`、`patch_path`、`patch_bytes`、`changed_files`、`verify`。

## 4. 权限边界

执行器不持有任何 Git 凭据，也不提交、不推送。它只做两件事：让模型在本地工作区改文件、
把改动整理成补丁。提交与推送仍然走服务端的 `repo.commit` / `repo.push` 门禁，
GitHub token 与 force-push 限制都在那一侧。模型进程拿不到仓库 token，也拿不到
蓝信凭据。

## 5. 测试与实测

单元测试（默认执行，`go test ./internal/codexrunner`）：

| 用例 | 结论 |
|---|---|
| `TestSpecValidateRejectsMissingFields` | 缺 `work_dir` / `prompt` 返回 `ErrInvalid` |
| `TestDiffIncludesTrackedAndNewFiles` | 改已有文件、加新文件都能进补丁 |
| `TestDiffHonorsGitignore` | `node_modules/`、`*.tmp` 被排除 |
| `TestDiffOnCleanWorkspaceIsEmpty` | 干净工作区返回空补丁 |

真实模型测试由 `SMARTPIPE_CODEX_LIVE=1` 门控，避免 CI 依赖模型配额。
2026-11-05 在本机用 `deepseek-v4-flash` 跑通了两级验证。

执行器级（`TestRunLiveCodex`）：PASS，5.4 秒完成一轮，模型按要求改了
`README.md`，`Diff` 拿到了改动。

命令行级（`codex-runner.exe`，临时仓库）：

```
{
  "ok": true,
  "status": "completed",
  "turn_id": "01a1ac20-e96b-7331-8888-9199ae39ce5f",
  "thread_id": "01a1ac20-e900-7b63-a48e-a6bf006ae338",
  "response": "已在 `README.md` 的列表末尾追加 `- item two`。未运行测试，未提交。",
  "patch_bytes": 147,
  "changed_files": ["README.md"],
  "duration_ms": 11524
}
```

补丁在干净基线上 `git apply --check` 通过。同一轮还暴露并修掉了一个真实缺陷：
任务描述文件写在 work-dir 里时会被 `git add -A` 收进补丁，现在 `Diff` 接受
`exclude` 参数，`codex-runner` 会把 `-prompt-file` 和 `-out` 自动排除，
`TestDiffSkipsExcludedPaths` 覆盖该行为。

模型名要走通需要注意可用性：`deepseek-v4-flash` 可用；`deepseek-flash` 被
litellm 以 403 `Model is blocked` 拒绝；`gpt-6.1-sol`、`deepseek-v4-pro` 被上游
判为 `Invalid model name`。这些都表现在模型边界，执行器代码无需改动。
## 6. 部署前提

`codex-runner` 依赖本机有 `codex` 可执行文件（默认取 `PATH`，可用
`-codex` 或 `SMARTPIPE_CODEX_BIN` 指定），并且该进程能拿到模型凭据。
Docker 镜像目前没有内置 codex CLI；要用容器跑，需要自己装
`@openai/codex` 并把登录态挂进容器。

## 7. 下一步

1. 把 `codex-runner` 接到 Issue 触发的队列上：Issue 建单 → 准备 workspace →
   跑一轮 → 拿补丁 → 调 `verify.patch` → 提交/推送 → 回写 Issue 与蓝信。
2. 在真实凭据下跑通一次完整链路，记录 Issue 编号、补丁哈希与验证报告。
3. 观察稳定后再决定是否引入 Syntropy 的 Agent 团队替换或并行于这条执行路径。