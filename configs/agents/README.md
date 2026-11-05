# 智能体角色定义

本目录存放 SmartPipe 的 Agent 角色契约。创建 Agent 时随任务注入 Syntropy，不进入
Syntropy 的 `data/templates`（集成边界见设计文档第 0 节）。

- `pipeline-lead/`：每个 Issue 团队的初始唯一 Agent，负责分诊、复杂度判断、派生、
  最终验证、提交、合并和用户沟通。
- `dev-engineer/`：按需派生的开发子 Agent 模板。
- `test-runner/`：按需派生的独立测试子 Agent 模板。
- `release-manager/`：按需派生的发版子 Agent 模板。
- `issue-triage/`：保留为可选的分诊子任务模板；主路径由 `pipeline-lead` 直接完成分诊。

复杂 Issue 的讨论确认规则见 `docs/复杂Issue讨论与确认流程.md`；无 GitHub Actions 的
开发、测试和合并规则见 `docs/无GitHub-Actions的Agent开发测试方案.md`。
