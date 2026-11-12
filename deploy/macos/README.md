# macOS 构建机接入（无需 SSH）

构建机是 **pull 模式**：这台 Mac 主动向 release-service 拉任务，不需要开端口、不需要 SSH 登录，也不需要把 Mac 的账号密码交给平台。

## 1. 准备二进制

在任意机器上交叉编译（签名与公证用的是 Mac 本机的钥匙串工具，交叉编译只负责产出二进制）：

```bash
cd backend
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o dist/release-executor-darwin-arm64 ./cmd/release-executor
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -o dist/release-executor-darwin-amd64 ./cmd/release-executor
```

把对应的二进制拷到 Mac（AirDrop、内网共享、scp 任选），例如 `/tmp/release-executor`。

## 2. 安装并常驻

```bash
sudo SMARTPIPE_RELEASE_SERVICE_URL=http://<release-service>:18099 \
     SMARTPIPE_RELEASE_SERVICE_TOKEN=<executor token> \
     SMARTPIPE_RELEASE_EXECUTOR_ID=mac-mini-01 \
     bash deploy/macos/install.sh /tmp/release-executor
```

脚本会：

- 把二进制装到 `/usr/local/bin/release-executor`；
- 写 `/usr/local/etc/smartpipe/release-executor.env`（0600 root:wheel）存放服务地址、执行器令牌与 id；
- 安装包装脚本 `/usr/local/bin/smartpipe-release-executor-runner`，由它加载 env 后 exec 执行器；
- 渲染并校验 `/Library/LaunchDaemons/com.smartpipe.release-executor.plist`（不含任何凭据），`launchctl bootstrap system` 注册常驻服务。

日志在 `/usr/local/var/log/smartpipe-release-executor.log`。改完 env 后执行 `sudo launchctl kickstart -k system/com.smartpipe.release-executor` 生效。

不装系统、只检查脚本与 plist：

```bash
bash deploy/macos/install.sh --dry-run /tmp/release-executor
```

它会打印安装位置、渲染 plist 并用 python3 plistlib 校验，不改系统。

## 3. 在控制台确认

- 执行器列表出现 `mac-mini-01`（`darwin/arm64` 或 `darwin/amd64`）。
- 发版计划的目标 `darwin-arm64` 会自动绑定到它，或通过 `executor_bindings` 指定。

## 4. 签名与公证

`smartpipe.yaml` 的目标上可以写 `sign:`，构建机在 build 之后、verify 与上传之前执行：

```yaml
release:
  targets:
    darwin-arm64:
      build: "xcodebuild -scheme App -configuration Release -archivePath build/App.xcarchive archive"
      sign: |
        codesign --force --deep --options runtime \
          --sign "Developer ID Application: <Team Name> (<TEAMID>)" \
          --timestamp build/App.app
      verify: "codesign --verify --deep --strict build/App.app"
      artifacts:
        - role: mac-app
          glob: build/App.app/Contents/MacOS/App
```

公证走 Apple 的两步指令（`xcrun notarytool submit ... --wait` + `xcrun stapler staple`），
证书与 App Store Connect 凭据放在 Mac 的钥匙串里，不要把 `.p12` 或 API key 放进仓库或 Agent 上下文。

配置了 `sign:` 的目标，上传的制品会被登记为 `platform_signed=true`；如果 `release.policies.signing.required: true`，
发布时发现未签名制品会直接以 `unsigned_artifacts` 拒绝推进。
