# 无 GitHub Actions 的 Agent 开发、测试与合并方案

日期：2026-11-05  
约束：不使用 GitHub Actions 分钟数，不依赖托管 CI；开发、测试、最终合并和提交由 Agent 完成。

## 1. 目标

在不使用 GitHub Actions 的前提下，仍然保证：

- dev 子 Agent 能独立开发并完成本地测试。
- test 子 Agent（可选）能在干净副本上做独立验证。
- lead 负责最终代码审查、权威验证、提交和合并。
- 每次合并都有可追溯的测试证据、工具链版本和补丁来源。
- 未通过验证、未获复杂 Issue 批准的代码不能进入主分支。

这套方案把“权威测试”从 GitHub Actions 移到 lead 控制的干净工作区；GitHub 仍然承载 Issue、讨论、PR、标签和 Release。

## 2. 角色边界

| 角色 | 做什么 | 不能做什么 |
|---|---|---|
| dev 子 Agent | 在任务工作区复现、修改、写单测、跑构建/静态检查/单测，回传 diff 和日志 | 不能推送主分支，不能合并，不能修改保护分支 |
| test 子 Agent（可选） | 在 base_commit + patch 的全新副本上跑集成/回归测试，独立给出报告 | 不能改业务代码，不能合并 |
| pipeline-lead | 判断方案、派生 Agent、最终审查 diff、在干净副本跑门禁、提交、合并、打标签、通知 | 不能跳过复杂 Issue 的审批状态；不能复用 dev 工作区作为最终验证环境 |
| 桥接服务 | 保存映射、审批、证据索引、审计；提供受限 git/工具接口 | 不替 Agent 做业务判断，不保存第二套 Issue 描述 |
| GitHub | Issue、评论、PR、标签、Release 记录 | 不作为测试执行环境 |

lead 是唯一拥有主分支写权限的 Agent。dev/test 只有只读仓库权限；即使它们生成了代码，也必须由 lead 在干净副本中重新验证后提交。

## 3. 仓库内的验证契约

仓库根目录放一个 `smartpipe.yaml`（或复用现有 `Makefile`），把测试命令固定下来，禁止每个 Agent 临时发明命令：

```yaml
version: 1
toolchain:
  go: "1.24"
  node: "22"
commands:
  build: "go build ./..."
  lint: "go vet ./..."
  unit: "go test ./..."
  integration: "go test -tags=integration ./..."
  smoke: "go run ./cmd/botdiag"
required_for_merge:
  - build
  - lint
  - unit
  - integration
timeouts:
  build: 5m
  unit: 10m
  integration: 20m
```

如果目标仓库不适合用 YAML，就固定使用 `Makefile` 的 `verify` 目标。lead 只执行合同中声明的命令；需要新增或修改测试命令时，走代码评审。

## 4. 开发到合并的流程

```
GitHub Issue（已批准或简单自动放行）
  → dev 子 Agent：
      从 base_commit 建独立工作区
      复现 → 修改 → 写测试 → 跑 build/lint/unit
      回传 patch + 自测日志 + 环境信息
  → test 子 Agent（可选）：
      从 base_commit 新建干净副本
      应用 patch → 跑 integration/smoke
      回传独立测试报告
  → lead 最终门禁：
      从 base_commit 新建干净副本
      校验 patch 可干净应用
      执行 required_for_merge 全部命令
      检查 diff 大小、敏感文件、密钥、依赖变化
      检查 Issue 审批状态和证据链
      通过 → 生成提交 → 合并主分支 → 推送
      失败 → 退回 dev，最多 3 轮
  → GitHub Issue / PR 写验证摘要
  → 蓝信通知用户
```

lead 的最终验证必须使用新的工作区，不能直接在 dev 的目录里跑完就合并。这样可以发现“dev 本地能跑、干净副本跑不了”的问题。

## 5. 分支与提交

- dev 产生的改动以 `base_commit + patch` 回传，不直接推主分支。
- lead 验证通过后，在 `agent/<issue-number>` 分支上生成提交，再合并到主分支。
- 提交信息固定关联 Issue，例如 `fix(#123): handle empty export response`。
- 默认使用 squash merge 或 fast-forward；禁止 force push 主分支。
- 主分支保护规则至少要求：只能由 lead 的 GitHub 身份写入、禁止直接推送、禁止删除、禁止 force push。
- 如果没有 GitHub Actions，PR 上可能没有自动检查状态；可以用桥接服务创建 GitHub Check Run（`smartpipe/verify`），也可以使用 `smartpipe:verified` 标签作为人工可见证据。Check Run 不依赖 Actions，只需要 GitHub App 的 Checks 权限。

## 6. 测试证据

每次最终验证生成结构化报告：

```json
{
  "issue": "123",
  "base_commit": "abc123",
  "patch_hash": "sha256:...",
  "commands": [
    {"name": "build", "exit_code": 0, "duration_ms": 1200},
    {"name": "unit", "exit_code": 0, "duration_ms": 8400}
  ],
  "toolchain": {"go": "1.24.3"},
  "verified_by": "pipeline-lead",
  "started_at": "...",
  "finished_at": "..."
}
```

- 报告和完整日志由桥接服务保存，数据库只存索引和哈希。
- GitHub Issue 评论只放摘要、命令结果和证据位置，不粘贴完整日志。
- 验证通过后才允许把 `smartpipe:verified` 标签加上；合并后再加 `smartpipe:merged`。

## 7. 环境与可复现性

没有托管 CI 后，测试环境更容易漂移，必须额外约束：

- lead 使用固定的干净工作区或专用测试机，不和 dev 共用缓存目录。
- 工具链版本写进仓库合同；依赖使用 lockfile；构建和测试尽量在容器中执行。
- 需要数据库、Redis 或外部依赖的集成测试，用仓库内的 `docker compose` 测试编排；起不来就标记 `blocked`，不能伪造通过。
- 记录操作系统、工具链版本、依赖锁文件哈希和测试开始/结束时间。
- 失败重试必须记录次数和原因；不能通过无限重试把不稳定测试“跑绿”。

如果没有专用机器，至少保证 lead 的最终验证目录是全新 clone，并在报告中明确环境与生产环境的差异。这个风险要保留在发布门禁里。

## 8. 发布也不依赖 Actions

- release 子 Agent 在干净工作区构建制品，生成 Release Note，调用 GitHub Releases API 创建标签和发布记录。
- 制品签名、上传和部署由发版工具执行；如果这些工具不可用，发布任务标记失败，不自动发布。
- 发布成功后回写 Issue 标签和蓝信通知；失败则保留制品和日志，支持回滚。
- 大版本仍然保留人工确认和回滚开关。

## 9. 验收用例

1. 简单 Bug：dev 修改并自测，lead 在干净副本验证通过后提交、合并，蓝信收到通知。
2. dev 自测通过但干净副本测试失败：lead 阻断合并，退回 dev，不产生主分支提交。
3. 第 3 轮仍失败：Issue 进入 `blocked`，通知人工介入。
4. 复杂 Issue 未 `/approve`：lead 无法派生 dev，工具返回 `approval_required`。
5. patch 无法干净应用：lead 拒绝合并，保留失败日志。
6. diff 包含敏感文件或密钥：lead 拒绝合并。
7. 验证报告缺少命令、退出码或 patch hash：不允许合并。
8. dev/test 尝试直接 push 主分支：被 GitHub 权限或分支保护拒绝。
9. release 构建失败：不创建 Release，不通知“发版成功”。
10. 同一蓝信消息重复到达：只创建一个 Issue 和一个最终合并记录。

## 10. 风险与补偿

| 风险 | 补偿 |
|---|---|
| 本机测试不等同于生产 CI | 使用干净副本、固定工具链、容器化和完整依赖编排；报告中明确环境差异 |
| lead Agent 误合并 | 提交前强制检查审批、patch hash、验证报告和敏感文件；主分支只给 lead 写权限；保留回滚 |
| dev 自测掩盖问题 | 可选 test 子 Agent 独立验证；lead 最终重新跑全套 required 命令 |
| 测试环境漂移 | 锁版本、锁依赖、容器化、记录环境指纹 |
| 无 PR 状态检查 | 桥接服务创建 Check Run，或使用 `smartpipe:verified` 标签和评论证据 |
| 计算资源不足 | 限制并发 Agent 数量、设置 TTL 和超时；简单任务由 lead 直接处理，减少派生 |

## 11. 最小落地顺序

1. 先让 lead 在干净副本上执行 `build + unit`，并生成验证报告。
2. 再让 dev 回传 patch，lead 应用 patch 后完成提交和合并。
3. 加 `integration` 和敏感文件扫描。
4. 加复杂 Issue 审批门禁。
5. 最后接 Release Note、制品和蓝信通知。

不引入 GitHub Actions 后，核心不是“少一个平台”，而是把测试证据和合并权限收拢到 lead 的最终门禁里；这条门禁必须可复现、可审计，不能只靠 lead 的口头结论。
