# GitHub 授权实施记录

日期：2026-11-08
范围：控制台内发起 GitHub App 创建与仓库安装，凭据自动回写；不再要求用户填写 Client ID 或手工创建 PAT。

## 1. 产品流程

1. 用户在「凭据与连接 → GitHub 接入」点「连接 GitHub」；
2. 控制台请求 `POST /admin/github/app/start`，后台生成一次性的 GitHub App Manifest；
3. 新窗口自动提交 Manifest，GitHub 在该窗口完成必需的账号登录和创建确认；
4. GitHub 回调 `/admin/github/app/callback`，后台兑换应用私钥、App ID 和应用信息；
5. 页面自动进入应用的仓库安装页，用户选择允许访问的仓库；
6. 安装完成后回调 `/admin/github/app/install-callback/{id}`，后台把 App ID、installation ID 和私钥加密保存；
7. 控制台轮询连接状态，成功后显示「已连接」，bridge 在需要时自动换取 installation token。

整个控制台侧流程由产品发起、回调和轮询完成。GitHub 强制要求账号登录与安装授权，因此登录页和安装确认由 GitHub 提供，不在 SmartPipe 内收集 GitHub 密码。

## 2. 接口

| 方法 | 路径 | 鉴权 | 作用 |
|---|---|---|---|
| POST | `/admin/github/app/start` | 管理员 | 创建一次性连接流程，返回 `setup_id` 和 `launch_url` |
| GET | `/admin/github/app/launch/{id}` | 一次性随机 ID | 返回自动提交 GitHub Manifest 的页面 |
| GET | `/admin/github/app/callback` | 一次性 state | 兑换 GitHub App 凭据，并跳转安装页 |
| GET | `/admin/github/app/install-callback/{id}` | 一次性随机 ID | 接收 `installation_id`，加密写入 `github_app` 凭据 |
| GET | `/admin/github/app/status/{id}` | 管理员 | 供控制台轮询 pending / created / installed / error / expired |

应用凭据写入全局 `github_app` 凭据，名称为 `github-app-managed`，metadata 包含
`app_id`、`installation_id`、`slug`、`owner`、`source=app_manifest`。私钥作为凭据密文保存，
接口只返回掩码。

Manifest 申请的默认权限为 Issues 写、Contents 写、Pull requests 写、Metadata 读，覆盖当前
建单、代码修改和后续 PR 流程所需的最小集合。

## 3. 网络与安全

- GitHub App Manifest 兑换地址是 `https://api.github.com/app-manifests/{code}/conversions`；
- 当前容器的 DNS 会把 `github.com` 解析到不可达的边缘 IP，`deploy/docker-compose.yml` 使用
  `extra_hosts` 映射到可用地址，支持 `SMARTPIPE_GITHUB_IP` 覆盖；
- 创建流程和安装回调使用 16 字节随机 ID/state，30 分钟过期；
- 应用私钥只允许服务端解密，浏览器、日志和管理接口都不暴露明文；
- App Manifest 回调和安装回调不依赖管理员 Cookie，但仍要求进入流程时生成的一次性随机 ID；
- 授权过程写入审计：`github.app.start`、`github.app.created`、`github.app.installed`。

## 4. 验证

- 后端单测验证 Manifest 兑换请求和响应解析；
- 前端 `npm run build` 通过；
- 线上接口 `POST /admin/github/app/start` 返回可访问的 `launch_url`；
- 启动页包含完整 Manifest、自动提交脚本和正确的回调地址；
- 真实安装 `smartpipe-740369` 后，控制台连接测试成功换取 installation token；
- 蓝信消息重试后创建 Issue #5，标签、机器人身份和蓝信群回执均验证通过。

旧的 OAuth Device Flow 接口仍保留作运维备用，但主界面不再展示 Client ID 和设备码输入。
