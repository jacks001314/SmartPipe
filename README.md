# SmartPipe

以 GitHub Issue 为工作项、由 Codex 执行器驱动的自动化开发-测试-发版管道。

## 架构

```
蓝信群消息
  → bot ingress（验签、去重、群→项目）
  → scheduler
      ├─ chat worker（只读）：问答 / 判定缺陷 / 建 Issue
      └─ fix worker（每 Issue 1 个，worktree）：改代码 → 自测
  → patch-verifier（干净副本 + smartpipe.yaml 必过命令）
  → admin-api 门禁：repo.commit / repo.push / 建 PR
  → 人工 review / 合并 → Issue 关闭 → worker 收尾
  → 蓝信群收到进展与结果
```

关键约束：

- 复杂 Issue 先在 GitHub Issue 评论区讨论，有权限的维护者 `/approve vN` 确认方案后才进入开发。
- 不使用 GitHub Actions；测试在干净副本上执行，结果由 patch-verifier 落库。
- Codex 执行器只改本地文件，不持有 Git 与蓝信凭据；提交、推送、发版都由 admin-api 门禁完成。
- 保留蓝信 ↔ GitHub ↔ Codex 的薄桥接、审批记录与审计；运行时编排、MCP 网关、Agent 令牌、
  派生与派单等早期链路已移除。

设计基线：[docs/自动化开发-测试-发版管道设计方案.md](docs/自动化开发-测试-发版管道设计方案.md)，
执行器细节见 [docs/Codex执行器实施记录.md](docs/Codex执行器实施记录.md)。

## 当前代码状态

| 组件 | 职责 | 入口 |
|---|---|---|
| admin-api | 配置台 API、项目/凭据/GitHub/蓝信/模型管理、提交推送与发版门禁、审计 | `backend/cmd/admin-api`、`web/` |
| bot | 蓝信 ingress：验签、去重、身份映射，转发到 bridge | `backend/cmd/bot` |
| bridge | GitHub webhook、消息 → Issue 薄桥接、`/approve` 审批链、崩溃上报入库 | `backend/cmd/bridge` |
| scheduler | 消息路由、建单去重、队列与并发、Issue↔蓝信双向同步、worker 生命周期 | `backend/cmd/scheduler` |
| patch-verifier | 干净副本应用补丁、执行验证命令、构建与发版制品 | `backend/cmd/patch-verifier` |
| codex-runner | 起 Codex app-server 跑一轮会话，产出 unified diff | `backend/cmd/codex-runner` |
| codex-pipeline | 把一条 Issue 走完整链路：改代码 → 验证 → 提交 → 推送 → 回写 Issue | `backend/cmd/codex-pipeline` |

## 构建与部署

```powershell
# Windows：前端 + 服务二进制 + 清单与 SHA-256
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\build.ps1 -Version 0.1.0
```

```bash
# Linux：Docker 一键安装（也可加 --install-docker）
sudo ./scripts/install.sh --mode docker --env-file deploy/.env

# 原地升级并等待所有健康检查
sudo ./scripts/deploy.sh

# 部署验收
sudo ./scripts/verify-deploy.sh --mode docker --install-dir /opt/smartpipe
```

Docker 部署五个常驻服务：`admin-api`、`bridge`、`patch-verifier`、`bot`、`scheduler`。
`codex-runner` 与 `codex-pipeline` 是命令行执行器，按需在宿主机运行。
支持 Docker Compose 与二进制 + systemd 两种部署方式；环境变量、远程 Windows 部署和排障说明见
[部署文档](deploy/README.md)。

## 配置管理台

`web/` 是 React + Vite 实现的配置中心，接真实管理 API：

- 项目：从已克隆的本地仓库创建项目，选择蓝信群与模型，支持描述与高级设置、项目删除
- GitHub：仓库浏览、Fork/私有筛选、邀请、克隆到本地目录（带进度）、设备码与 OAuth 登录、代理设置
- 凭据与连接：AES-256-GCM 密文存储、掩码展示、轮换、服务端连接测试
- 蓝信：应用连接、群绑定、入站消息
- 模型：供应商与模型档案，项目级默认模型
- 发版与审计：发版运行、回滚、操作审计

![配置管理台](docs/images/console-projects.png)

启动方式与环境变量见 [M0 实施记录](docs/M0实施记录.md)。

## 文档

- [蓝信机器人使用说明（群成员版）](docs/蓝信机器人使用说明.md)
- [Electron 跨平台发版设计](docs/Electron跨平台发版设计.md)
- [主设计方案 v0.7.2](docs/自动化开发-测试-发版管道设计方案.md)
- [复杂 Issue 讨论与确认流程](docs/复杂Issue讨论与确认流程.md)
- [无 GitHub Actions 的 Agent 开发测试方案](docs/无GitHub-Actions的Agent开发测试方案.md)
- [GitHub Issue 驱动架构](docs/GitHub-Issue驱动架构.md)
- [集中配置管理台设计](docs/集中配置管理台设计.md)
- [Codex 执行器实施记录](docs/Codex执行器实施记录.md)
- [崩溃上报实施记录](docs/崩溃上报实施记录.md)
- [下一步实施计划](docs/下一步工作计划.md)
- [M0 实施记录](docs/M0实施记录.md)
