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
  "host": "build-01.example.com",
  "port": 22,
  "username": "<ssh-user>",
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
| `SMARTPIPE_RELEASE_SERVICE_PUBLIC_URL` | 构建机访问 release-service 的地址，例如 `https://smartpipe.example.com:8099`。不填时用管理员访问的主机名 + `SMARTPIPE_RELEASE_SERVICE_PORT` |
| `SMARTPIPE_RELEASE_SERVICE_PORT` | 对外发布端口，默认 8099 |
| `SMARTPIPE_EXECUTOR_ASSETS` | 执行器二进制目录，默认 `/app/bin/executors` |

接入命令和 SSH 部署共用同一个地址解析逻辑，不会再出现把容器内 `http://worker:8095` 发给构建机的问题。
令牌统一取 `SMARTPIPE_RELEASE_SERVICE_TOKEN`（回退 `RELEASE_SERVICE_TOKEN`），与 release-service 校验的一致。

## 镜像改动

`Dockerfile` 的构建阶段会额外交叉编译 5 个平台的执行器：`linux/amd64`、`linux/arm64`、`darwin/amd64`、
`darwin/arm64`、`windows/amd64`，统一放在 `/app/bin/executors/<os>-<arch>/`。

## 验证记录（2026-11-13）

- 目标机：`<ssh-user>@<build-host>`（macOS，amd64），由配置台发起。
- 结果：SSH 连接、平台识别 `darwin/amd64`、上传 6 MiB 执行器、写入配置、`launchd` 启动、注册在线，总耗时 8 秒。
- 控制台执行器列表显示 `build-03 darwin/amd64 online`。
- 本机 `build-05`（linux/amd64）也验证过完整流程，用于排查令牌问题后已清理。

## 限制

- Windows 构建机暂不支持 SSH 自动部署，接口会提示改用手动接入命令。
- macOS 的 launchd 需要该用户具备可用的登录会话；不可用时自动回退到 `nohup`，机器重启后需要重新部署。
- 卸载：停止对应 launchd/systemd 服务后删除 `$HOME/.smartpipe/release-executor`。

## 已知环境坑：macOS 12 + Node 24 原生模块

macOS 12 自带的 libc++ 没有 `std::ranges`，而 Node 24 的 `v8-memory-span.h` 会按
`#if __has_include(<ranges>)` 给 `std::ranges::enable_view` 写特化。用 SDK 13.1 +
clang 14 编译时会出现两类错误：

- `no member named 'enable_view' in namespace 'std::ranges'`
- `redefinition of 'enable_view'`（先包含 `<string>` 再包含 v8 头时）

这是 Clang 14 对限定式变量模板特化的限制，不是项目代码问题。构建机上可对
node-gyp 头缓存做一次本地补丁，让该分支不参与编译：

```sh
for f in "$HOME"/Library/Caches/node-gyp/*/include/node/v8-memory-span.h; do
  [ -f "$f" ] || continue
  grep -q 'smartpipe: no ranges' "$f" && continue
  cp "$f" "$f.smartpipe.bak"
  sed -i '' 's|#if __has_include(<ranges>)|#if 0 /* smartpipe: no ranges on macOS 12 CLT */|' "$f"
done
```

补丁只影响当前用户缓存，不改系统 SDK；node-gyp 重新下载头时需要再执行一次。
`executor_probe` 会输出 `xcode-select -p`、SDK 版本、SDK 路径与 clang 版本，便于先确认
是否命中这个坑。更彻底的方案是把构建机升到 macOS 13+ 与 CLT 15+。

## 已知环境坑：macOS 构建机出 Windows 安装包

Syntropy 的 Windows 安装包可以在 Intel macOS 上交叉构建，但要求 PATH 里有
`makensis` 与 `x86_64-w64-mingw32-g++`；缺任何一个，`build.mjs` 会在
`assertInstallerTarget` 处直接失败。macOS 12 自带的老 Homebrew 已经跑不起来
（portable-ruby 2.3.7 读不到系统版本），不要指望 `brew install`。

已验证可行的替代装法（全部落在用户目录，`/usr/local/bin` 只放软链或包装脚本）：

```sh
# NSIS：用 electron-builder 的 macOS 版 makensis（3.04）
mkdir -p ~/toolchains/nsis
curl -fsSL -o /tmp/nsis.7z \
  https://github.com/electron-userland/electron-builder-binaries/releases/download/nsis-3.0.4.1/nsis-3.0.4.1.7z
bsdtar -xf /tmp/nsis.7z -C ~/toolchains/nsis
chmod +x ~/toolchains/nsis/mac/makensis
ln -sf ~/toolchains/nsis/mac/makensis /usr/local/bin/makensis
ln -sfn ~/toolchains/nsis /usr/local/share/nsis   # makensis 默认去这里找 Stubs/

# mingw-w64：用 llvm-mingw 的 clang++ 冒充 g++ 入口
curl -fsSL -o ~/toolchains/llvm-mingw.tar.xz \
  https://github.com/mstorsjo/llvm-mingw/releases/download/20261104/llvm-mingw-20261104-ucrt-macos-universal.tar.xz
tar -xf ~/toolchains/llvm-mingw.tar.xz -C ~/toolchains
cat > /usr/local/bin/x86_64-w64-mingw32-g++ <<'EOF'
#!/bin/sh
exec "$HOME/toolchains/llvm-mingw-20261104-ucrt-macos-universal/bin/x86_64-w64-mingw32-clang++" "$@"
EOF
chmod +x /usr/local/bin/x86_64-w64-mingw32-g++
```

验收（两条都要有输出）：

```sh
makensis -VERSION
x86_64-w64-mingw32-g++ --version | head -1
```

`executor_probe` 现在会额外输出 `windows cross toolchain:` 段落，Agent 可以先看这一段再决定是否重装。

### makensis 报 `std::bad_alloc`：构建命令没有 UTF-8 locale

现象：`makensis` 一读到带 `Unicode true` 的 `.nsi` 就
`libc++abi: terminating with uncaught exception of type std::bad_alloc`，
换成 NSIS 3.0.5 也一样；不带 `Unicode true` 的最小脚本却能编译。

根因不是内存、也不是 NSIS 版本：通过 SSH 启动的执行器没有 `LANG`，
并且继承了 `LC_CTYPE=C`，NSIS 的 Unicode 构建在这种情况下会直接崩溃。

验证方式（构建机上）：

```sh
env -i PATH=/usr/local/bin:/usr/bin:/bin LANG=en_US.UTF-8 makensis -V2 /tmp/u.nsi   # 成功
env -i PATH=/usr/local/bin:/usr/bin:/bin LANG=en_US.UTF-8 LC_CTYPE=C makensis -V2 /tmp/u.nsi  # bad_alloc
```

执行器自 2026-11-15 起在构建命令和 Agent 遥控命令里统一补
`LANG` / `LC_ALL`（macOS 用 `en_US.UTF-8`，Linux 用 `C.UTF-8`），
只在环境里没有有效 UTF-8 locale 时生效；`executor_probe` 的 `locale:` 行会显示当前值。

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

验证（2026-11-13）：在 `<ssh-user>@<build-host>` 上部署并删除两个临时执行器——`build-03-test2`（`$HOME/.smartpipe/executor-test2`）和 `build-03-test3`（绝对路径 `/tmp/smartpipe-executor-test3`），两次都返回 `remote_cleaned: true`，步骤为“停止并卸载 → 删除注册记录 → 删除 SSH 资源”，`build-03` 保持在线未受影响。绝对路径场景下进程清理按 `^<dir>/release-executor` 精确匹配，避免误杀执行脚本自身的 shell。
