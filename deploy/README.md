# 部署

当前仓库里的 `bot.env.example` 和 `bot.local.env` 只用于早期本地原型。

目标方案中，GitHub、蓝信和 Syntropy 的连接与凭据由 [集中配置管理台](../docs/集中配置管理台设计.md) 统一管理：

- 项目、GitHub 仓库地址、默认分支和蓝信群绑定在界面配置。
- GitHub App、Webhook secret、蓝信 App Secret、Syntropy service token 加密存储。
- Agent 只接收 `project_id`，不接触 token 明文。
- 部署环境只保留管理台主密钥、数据库连接和管理员引导凭据，不保存业务 token 明文。

连通性自检（只读，不发消息）：

```powershell
# 本地原型：加载 bot.local.env 后运行
Get-Content deploy/bot.local.env | ForEach-Object { if ($_ -match '^\s*([A-Z_]+)=(.*)$') { [Environment]::SetEnvironmentVariable($Matches[1], $Matches[2].Trim()) } }
Set-Location backend
go run ./cmd/botdiag
```
