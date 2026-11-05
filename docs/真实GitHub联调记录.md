# 真实 GitHub 联调记录

日期：2026-11-05。仓库：https://github.com/jacks001314/SmartPipe（public）。

## 1. 环境

| 项 | 值 |
| --- | --- |
| 仓库 | `jacks001314/SmartPipe`，默认分支 `main` |
| 凭据 | GitHub CLI 已登录账号（`repo` 权限），写入配置台的 `github_pat` 凭据（AES-GCM 加密） |
| Webhook | `https://<cloudflared 临时域名>/webhooks/github`，事件 `issues` + `issue_comment` |
| 项目 | `prj_a21c063d`（名称「SmartPipe (GitHub 真实)」，群 `grp-smartpipe`） |

因为本机在 NAT 后面，webhook 用 cloudflared 临时隧道回源到 bridge :8090：

```powershell
.dev\tools\cloudflared.exe tunnel --no-autoupdate --url http://127.0.0.1:8090
# 取到 https://<random>.trycloudflare.com 后，用它创建仓库 webhook（secret 与 bridge 的 GITHUB_WEBHOOK_SECRET 一致）
```

## 2. 已验证的链路（均为真实 GitHub 上的操作）

| 步骤 | 结果 |
| --- | --- |
| 蓝信消息 → 建单 | Issue #2《真实联调：启动后闪退》，自动打 `source:bluex`、`smartpipe` |
| Webhook 验签与处理 | 仓库评论 `/plan ...` → bridge 登记方案 v1（归档指纹 sha256:fd7b166d…） |
| 门禁（未批准） | MCP `verify.patch` 返回 `approval_required: plan v1 has no approval yet` |
| `/approve v1` | 评论触发批准，Issue 自动出现 `needs-approval`、`approved` 标签 |
| 批准后验证 | `verify.patch` 在干净副本上 `git apply` + 执行 `smartpipe.yaml` 的 build，`report.ok=true`，check build exit 0 |
| 分支推送 | `repo.commit`/`repo.push` 推出 `agent/real-181046`（`d8e96be`），远端 HEAD 与提交一致 |
| 发版 | v0.1.0 创建 tag 与 Release；v0.1.1 附件 `admin-api.exe`（32.8 MB）上传成功，带下载链接 |
| 标签回写 | 发版后 Issue 从 `smartpipe:ready` 改为 `smartpipe:released`，标签不存在时自动创建 |

## 3. 真实环境暴露出的三个 bug（已修）

1. **Release 附件上传 404**：GitHub 的上传端点在 `uploads.github.com`，代码却拼在 `api.github.com` 上。
   现在 `Client.uploadBase()` 会把 `api.github.com` 换成 `uploads.github.com`，企业版把 `api.` 换成 `uploads.`，本地假服务保持原地址。
2. **给已有 Issue 打不存在的标签返回 422**：假 GitHub 会自动建标签，真 GitHub 不会。
   `AddLabels` 现在遇到 422 会先 `EnsureLabel`（GET → 404 则 POST 创建）再重试一次。
3. **私有仓库发版构建无法克隆**：`release.Build` 用裸 `git clone`，拿不到凭据。
   现在 `RunRequest.CloneURL` 复用推送用的鉴权地址（服务端注入 token，不进 Agent）。本次仓库改为 public 后该路径仍保留，供私有仓库使用。

## 4. 已知限制

- cloudflared 临时隧道的域名每次启动都变，重启后需同步更新仓库 webhook；正式环境建议固定域名或反代。
- 当前仓库里的 Issue #2、tag v0.1.0/v0.1.1 是联调产物，可随时删除。
- 发版构建在 admin-api 所在主机上执行，需要那台机器安装了目标项目的工具链（此处为 Go）。
