# SmartPipe 构建、安装与部署

SmartPipe 在生产上跑两个容器，共用同一个镜像和同一个 `/data` 卷：

| 容器 | 进程 | 端口 | 作用 |
|---|---|---:|---|
| `app` | `admin-api` | 8092 | 管理接口、Web 控制台、数据库迁移 |
| `app` | `bridge` | 8090 | 蓝信消息与 GitHub Webhook 入口 |
| `app` | `bot` | 8093 | 蓝信 WebSocket、群回复、崩溃上报 |
| `worker` | `scheduler` | 8098（仅容器内） | 队列、worktree、Codex、提交、推送、开 PR |
| `worker` | `patch-verifier` | 8095（仅容器内） | 发版构建与手动验证 |

镜像内带 Git、Go 1.26、Node 22 和 Codex CLI。Issue 修复链路由 Codex 在 worker 容器里改代码并
自行执行 `smartpipe.yaml` 的必过命令，调度器提交后经 admin-api 推送，不再经过独立验证容器。
`patch-verifier` 只保留给发版构建和配置台的手动验证。

## 1. 构建 Windows 产物

在仓库根目录运行：

```powershell
$env:GOPROXY = "https://goproxy.cn,direct"
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\build.ps1 `
  -Version 0.1.0 -OutputDir dist
```

输出位于 `dist/`：

- `bin/admin-api.exe`、`bridge.exe`、`patch-verifier.exe`、`bot.exe`、`botdiag.exe`
- `web/`：已构建的 React 静态站点
- `manifest.json`：版本、Git commit、构建时间和每个二进制的 SHA-256
- `SHA256SUMS`
- `smartpipe-<version>-windows-amd64.zip`

交叉编译 Linux 产物：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\build.ps1 `
  -Version 0.1.0 -TargetOS linux -Arch amd64 -OutputDir dist-linux
```

## 2. Linux 构建

```bash
./scripts/build.sh --version 0.1.0 --os linux --arch amd64
```

产物同样在 `dist/`，并生成 `smartpipe-<version>-linux-amd64.tar.gz`。脚本会先执行 `npm ci` 和前端构建；本地已构建过前端时可加 `--skip-web`。

## 3. Docker 安装（推荐）

目标机需要 Docker Engine 和 Docker Compose v2。若目标机尚未安装 Docker，可让安装脚本调用 Docker 官方安装脚本：

```bash
sudo ./scripts/install.sh --install-docker \
  --dsn 'postgres://smartpipe:password@192.168.1.99:5432/smartpipe?sslmode=disable' \
  --lanxin-app-id 'orgId-appId' \
  --lanxin-app-secret 'your-app-secret'
```

没有现成 `deploy/.env` 时，安装脚本会生成：

- 32 字节随机 AES-256-GCM 主密钥
- 管理、bridge、验证、发版、bot、崩溃上报接口的独立随机令牌
- GitHub Webhook secret
- `deploy/.env`，权限设置为 `600`

安装动作会：

1. 安装/更新代码到 `/opt/smartpipe`；
2. 创建 `deploy/data/{verify,workspaces,releases,go,go-cache,npm}`；
3. 校验 Compose 配置并构建 `smartpipe:local`；
4. 启动五个服务；
5. 等待全部健康检查通过。

也可以先手写环境文件再安装：

```bash
cp deploy/.env.example deploy/.env
chmod 600 deploy/.env
# 编辑 deploy/.env，至少替换 DSN、主密钥、全部内部令牌和蓝信凭据
sudo ./scripts/install.sh --mode docker --env-file deploy/.env
```

安装后检查：

```bash
cd /opt/smartpipe/deploy
sudo docker compose ps
curl -fsS http://127.0.0.1:8092/healthz
curl -fsS http://127.0.0.1:8090/healthz
curl -fsS http://127.0.0.1:8095/healthz
curl -fsS http://127.0.0.1:8097/healthz
curl -fsS http://127.0.0.1:8093/healthz
```

控制台地址为 `http://<主机>:8092/`。已有 `deploy/.env` 时，升级会保留它和 `deploy/data/`。

## 4. 部署验收

```bash
# 检查环境文件权限/主密钥/内部令牌，并检查全部容器和 healthz
sudo ./scripts/verify-deploy.sh --mode docker --install-dir /opt/smartpipe

# 只校验环境文件，不访问服务
sudo ./scripts/verify-deploy.sh --secrets-only --env-file /opt/smartpipe/deploy/.env
```

二进制部署使用 `--mode binary --install-dir /opt/smartpipe`，脚本会检查五个 systemd 单元和默认端口。

## 5. Docker 日常更新

在目标机仓库目录执行：

```bash
sudo ./scripts/deploy.sh
```

仅用已构建镜像重启：

```bash
sudo ./scripts/deploy.sh --no-build
```

查看日志：

```bash
cd /opt/smartpipe/deploy
sudo docker compose logs -f --tail=200
```

停止但保留数据：

```bash
cd /opt/smartpipe/deploy
sudo docker compose down
```

## 6. 从 Windows 远程部署

`deploy.ps1` 会打包当前工作区、上传到 Linux、解包并执行安装脚本。SSH 密码通过 `-PassFile` 或环境变量 `SMARTPIPE_SSH_PASSWORD` 提供；密码不会打印。

SSH 端口不是 22 时，加 `-Port 2222`。

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\deploy.ps1 `
  -HostName 192.168.1.99 -User jacks `
  -PassFile C:\secure\smartpipe-ssh.txt `
  -EnvFile .\deploy\.env
```

远程 Docker 构建：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\deploy.ps1 `
  -HostName 192.168.1.99 -User jacks `
  -PassFile C:\secure\smartpipe-ssh.txt `
  -EnvFile .\deploy\.env -Mode docker
```

远程二进制部署：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\deploy.ps1 `
  -HostName 192.168.1.99 -User jacks `
  -PassFile C:\secure\smartpipe-ssh.txt `
  -EnvFile .\deploy\.env -Mode binary
```

二进制模式会先在 Windows 上交叉编译 Linux 产物，再上传并以 systemd 安装。

## 7. 二进制与 systemd 安装

```bash
./scripts/build.sh --os linux --arch amd64
sudo ./scripts/install.sh --mode binary --bundle dist --env-file deploy/.env
```

安装位置：

- 程序：`/opt/smartpipe/bin/`
- 前端：`/opt/smartpipe/web/`
- 环境文件：`/etc/smartpipe/smartpipe.env`（`600`）
- 数据：`/var/lib/smartpipe/{runtime,verify,releases,go,go-cache,npm}`

单元文件位于 `deploy/systemd/`。安装脚本会替换其中的 `@INSTALL_DIR@` 和 `@STATE_DIR@`，再执行 `systemctl enable --now`。

## 8. 部署前检查

```bash
bash -n scripts/build.sh scripts/install.sh scripts/deploy.sh
docker compose -f deploy/docker-compose.yml --env-file deploy/.env config -q
```

完整本地回归仍使用：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\sit-all.ps1
```

## 9. 安全与运维注意

- `deploy/.env`、`deploy/data/` 和 `.dev/` 均不得提交到 Git。
- 主密钥必须备份；丢失后，配置台中保存的第三方凭据无法解密。
- `SMARTPIPE_BIND_ADDR=0.0.0.0` 会监听所有网卡，建议用防火墙限制 8090/8092/8095/8097/8093。
- GitHub Webhook 暴露到公网时，使用反向代理或临时隧道，并保持 `GITHUB_WEBHOOK_SECRET` 已配置。
- 控制台登录：设置 `SMARTPIPE_ADMIN_USER` / `SMARTPIPE_ADMIN_PASSWORD` 后启用用户名密码登录，
  会话保存在 HttpOnly Cookie（`SMARTPIPE_SESSION_TTL`，默认 12h）；密码留空时只接受
  `Authorization: Bearer $SMARTPIPE_ADMIN_TOKEN`。修改密码后可用
  `SMARTPIPE_ADMIN_PASSWORD_RESET=true` 重启一次强制同步环境变量里的新密码。
- 数据库迁移由 `admin-api` 和 `bridge` 启动时幂等执行；多个实例同时启动由 PostgreSQL advisory lock 串行化。
- 升级前建议先备份数据库；`deploy/data/` 保存运行时实例、验证工作区和发版制品。
## 10. 同一台机器跑第二套实例

`deploy/docker-compose.yml` 里写了 `name: smartpipe`，默认会占用同一个 compose
项目名；在已有实例的机器上再装一套，必须同时满足三点，否则会把旧实例的容器重建：

1. 环境文件里加 `COMPOSE_PROJECT_NAME=smartpipe-test2`（compose 优先读这个变量）；
2. 换一组端口：`SMARTPIPE_ADMIN_PORT` / `BRIDGE_PORT` / `BOT_PORT` / `VERIFY_PORT` 全部改掉；
3. 换一个数据库：不要和旧实例共用同一个 database，否则迁移与数据互相干扰。

示例（192.168.0.5 上的第二套，端口 19090–19097）：

```bash
# 数据库：用已有 Postgres 容器建独立库与角色
docker exec smartpipe-postgres psql -U smartpipe -d postgres -c "CREATE ROLE smartpipe_v05 LOGIN PASSWORD '...'"
docker exec smartpipe-postgres psql -U smartpipe -d postgres -c "CREATE DATABASE smartpipe_v05 OWNER smartpipe_v05"

# 环境文件追加
COMPOSE_PROJECT_NAME=smartpipe-v05
SMARTPIPE_DSN=postgres://smartpipe_v05:...@172.17.0.1:5432/smartpipe_v05?sslmode=disable
SMARTPIPE_ADMIN_PORT=19092
SMARTPIPE_BRIDGE_PORT=19090
SMARTPIPE_BOT_PORT=19093
SMARTPIPE_VERIFY_PORT=19095
```

容器里的服务走 `172.17.0.1`（docker0 网关）访问宿主机上已经发布到 `0.0.0.0:5432`
的 Postgres。

另外，全新数据库必须依赖 `store.Migrate` 按版本升序执行迁移；旧代码按声明顺序
（18 → 1）执行，空库会在 `agent_tokens` 上失败，admin-api 反复重启。该问题已修复，
排障时先看 `docker logs <project>-admin-api-1` 里有没有 `apply migration 18`。
