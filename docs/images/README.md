# 架构图说明

`pipeline-architecture.drawio` 对应旧版 v0.6.2（Orchestrator 状态机、四个常驻 Agent、内部 Issue 服务、GitHub Actions 权威测试），已不代表当前方案。

当前架构以 [主设计方案 v0.7.1](../自动化开发-测试-发版管道设计方案.md) 中的 Mermaid 图和组件表为准：

- GitHub Issue 是工作项事实源。
- 每个 Issue 一个 `pipeline-lead`，按需派生 dev/test/release。
- 复杂 Issue 先在 GitHub 评论区讨论并确认具体方案版本。
- 不使用 GitHub Actions，lead 在干净副本做最终验证、提交和合并。

旧 draw.io 文件仅保留作历史参考；需要对外展示时，应按 v0.7.1 重绘。
