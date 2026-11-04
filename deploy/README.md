# 部署

- `bot.env.example`：环境变量模板（可提交）
- `bot.local.env`：本地真实凭证（已被 .gitignore 排除，勿提交）

连通性自检（只读，不发消息）：

```powershell
# PowerShell：加载本地配置后运行
Get-Content deploy/bot.local.env | ForEach-Object { if ($_ -match '^\s*([A-Z_]+)=(.*)$') { [Environment]::SetEnvironmentVariable($Matches[1], $Matches[2].Trim()) } }
Set-Location backend
go run ./cmd/botdiag
```
