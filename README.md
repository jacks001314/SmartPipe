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

## 本地运行（M1 端到端）

三个终端，顺序启动：

```powershell
# 1) Issue 管理服务（内存存储）
Set-Location backend; go run ./cmd/issue-service

# 2) 编排服务
$env:ISSUE_SERVICE_URL = 'http://127.0.0.1:8092'
$env:BOT_URL = 'http://127.0.0.1:8091'
$env:INTERNAL_TOKEN = 'dev-token'
go run ./cmd/orchestrator

# 3) 蓝信机器人（凭证见 deploy/bot.local.env）
$env:ORCHESTRATOR_URL = 'http://127.0.0.1:8090'
$env:INTERNAL_TOKEN = 'dev-token'
# 加载 LANXIN_APP_ID / LANXIN_APP_SECRET / LANXIN_API_BASE 后：
go run ./cmd/bot
```

群内 @机器人 提问 -> Orchestrator 归档 -> 蓝信收到 Issue 编号回复。
连通性自检（只读）：`go run ./cmd/botdiag`。
