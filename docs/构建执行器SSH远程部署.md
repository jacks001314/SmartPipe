# 构建执行器 SSH 远程部署

发版 v2 的「构建执行器」不再只给一段需要人工粘贴的命令。配置台可以直接用 SSH 连接构建机，把
`release-executor` 装上去、写好服务地址与令牌、启动并等待注册，流程与 Syntropy 的“创建并部署节点”一致。

## 使用方式

1. 打开 `运行中心 / 发版 v2`，点「＋ 添加构建机」。
2. 填写构建机 id、系统与架构（默认“自动检测”），再填 SSH 主机、端口、用户名、密码或私钥。
3. 点「SSH 部署并注册」。页面按步骤显示：SSH 连接、平台识别、上传执行器、写入配置、启动、注册结果。
4. 勾选“保存为 SSH 资源”时，连接信息会以 `build`、`release` 标签写入资源库，后续可复用。

「生成手动接入命令」仍然保留，适合无法 SSH 的机器或离线安装。

## 服务端接口

`POST /admin/releases/v2/executors/deploy`

```json
{
  "id": "build-03",
  "os": "auto",
  "arch": "auto",
  "host": "192.168.0.3",
  "port": 22,
  "username": "malama",
  "auth_type": "password",
  "secret": "……",
  "install_dir": "",
  "save_resource": true
}
```

也可以用 `resource_id` 引用资源库里已有的 SSH 资源，此时不需要传明文密码。

返回结果包含 `ok`、`steps`、`registered`，失败时带 `error` 与远端日志尾部 `log_tail`。令牌和密码不会出现在
返回值里。

## 服务端做了什么

1. 用 `resourceprobe` 连接 SSH（支持密码、私钥、主机指纹固定）。
2. 执行 `uname -s` / `uname -m` 识别平台；与表单选择不一致时直接报错。
3. 从镜像内 `/app/bin/executors/<os>-<arch>/release-executor` 取对应平台二进制，经 SSH 上传。
4. 在 `$HOME/.smartpipe/release-executor`（或指定目录）写入：
   - `release-executor`：执行器二进制
   - `executor.env`：服务地址、令牌、执行器 id 与平台（权限 600）
   - `run.sh`：读取 env 后启动执行器
5. 以服务方式启动：
   - macOS：`~/Library/LaunchAgents/com.smartpipe.release-executor.<id>.plist`，`launchctl load -w`
   - Linux：`systemctl --user enable --now smartpipe-release-executor-<id>.service`
   - 两者都不可用时回退到 `nohup`，进程号写入 `executor.pid`
6. 轮询数据库最多 45 秒，确认执行器心跳在线。

## 配置项

| 变量 | 作用 |
|---|---|
| `SMARTPIPE_RELEASE_SERVICE_PUBLIC_URL` | 构建机访问 release-service 的地址，例如 `http://192.168.0.5:8099`。不填时用管理员访问的主机名 + `SMARTPIPE_RELEASE_SERVICE_PORT` |
| `SMARTPIPE_RELEASE_SERVICE_PORT` | 对外发布端口，默认 8099 |
| `SMARTPIPE_EXECUTOR_ASSETS` | 执行器二进制目录，默认 `/app/bin/executors` |

接入命令和 SSH 部署共用同一个地址解析逻辑，不会再出现把容器内 `http://worker:8095` 发给构建机的问题。
令牌统一取 `SMARTPIPE_RELEASE_SERVICE_TOKEN`（回退 `RELEASE_SERVICE_TOKEN`），与 release-service 校验的一致。

## 镜像改动

`Dockerfile` 的构建阶段会额外交叉编译 5 个平台的执行器：`linux/amd64`、`linux/arm64`、`darwin/amd64`、
`darwin/arm64`、`windows/amd64`，统一放在 `/app/bin/executors/<os>-<arch>/`。

## 验证记录（2026-11-13）

- 目标机：`malama@192.168.0.3`（macOS，amd64），由配置台发起。
- 结果：SSH 连接、平台识别 `darwin/amd64`、上传 6 MiB 执行器、写入配置、`launchd` 启动、注册在线，总耗时 8 秒。
- 控制台执行器列表显示 `build-03 darwin/amd64 online`。
- 本机 `build-05`（linux/amd64）也验证过完整流程，用于排查令牌问题后已清理。

## 限制

- Windows 构建机暂不支持 SSH 自动部署，接口会提示改用手动接入命令。
- macOS 的 launchd 需要该用户具备可用的登录会话；不可用时自动回退到 `nohup`，机器重启后需要重新部署。
- 卸载：停止对应 launchd/systemd 服务后删除 `$HOME/.smartpipe/release-executor`。

## 删除构建机

执行器列表右侧有「删除」按钮。删除不是只清数据库，而是回目标机做一次卸载：

1. 有 target 正在使用该执行器（leased / building / verifying）时先拦截，返回 409，避免拆掉正在跑的构建；确认后可以强制删除。
2. 按部署记录找到 SSH 资源和安装目录，连上去停止执行器：
   - macOS：`launchctl unload` 并删除 LaunchAgent plist
   - Linux：`systemctl --user disable --now` 并删除 unit
   - 兜底：读 `executor.pid` 结束进程，再 `pkill -f <dir>/release-executor`
3. 删除安装目录（脚本会先确认目录里有 `run.sh` 或执行器二进制，拒绝删除 `$HOME`、`/` 这类路径）。
4. 删除 `release_v2_executors` 注册记录和部署记录；如果 SSH 资源是部署时自动创建的（名称等于 `构建机 <id>`），一并删除。

远程卸载失败时不会静默丢记录：返回失败步骤并保留注册信息，界面会再确认一次，确认后走强制删除，只清理注册记录。

接口：`DELETE /admin/releases/v2/executors/{id}`，可选 body `{"force":true,"resource_id":"...","install_dir":"..."}`。
部署位置记录在迁移 44 新增的 `release_v2_executor_hosts` 表；本功能上线前部署的执行器没有这条记录，删除时可以带 `resource_id` / `install_dir`，或者强制只删注册信息。

验证（2026-11-13）：在 `malama@192.168.0.3` 上部署并删除两个临时执行器——`build-03-test2`（`$HOME/.smartpipe/executor-test2`）和 `build-03-test3`（绝对路径 `/tmp/smartpipe-executor-test3`），两次都返回 `remote_cleaned: true`，步骤为“停止并卸载 → 删除注册记录 → 删除 SSH 资源”，`build-03` 保持在线未受影响。绝对路径场景下进程清理按 `^<dir>/release-executor` 精确匹配，避免误杀执行脚本自身的 shell。
