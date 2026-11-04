# SmartPipe

内部自动化开发-测试-发版管道。设计文档见 [docs/自动化开发-测试-发版管道设计方案.md](docs/自动化开发-测试-发版管道设计方案.md)。

## 目录结构

```
sdks/       独立 SDK（lanxin：蓝信个人机器人 Go SDK，backend 通过 replace 引用）
backend/    Go 后端（单模块多服务：cmd/ 入口 + internal/ 实现）
  cmd/bot             蓝信机器人服务
  cmd/orchestrator    Orchestrator 编排服务
  cmd/issue-service   Issue 管理服务
  cmd/patch-verifier  补丁校验服务
  cmd/ci-adapter      CI/测试适配层
  cmd/release-service 发版服务
  cmd/mcp-gateway     MCP 网关
  internal/           各服务实现包（platform 为公共库，syntropy 为平台驱动适配层）
configs/    智能体角色定义（AGENTS.md 等，随任务注入 Syntropy）
web/        管理看板前端（React + TypeScript + Vite，M2+ 启用）
deploy/     部署编排（docker-compose 骨架）
docs/       设计文档与架构图源文件
```

> 技术选型依据见设计文档第 10 节：后端全 Go（复用 Syntropy `api/client`），前端 React + TS。
