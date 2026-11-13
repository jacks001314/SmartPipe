# SmartPipe 自有桌面模块部署

只部署本目录Node REPL/CUA模块，不安装Qwen Code CLI，不使用它的模型、账号或调度系统。

在test-runner目录依次运行：

```text
npm ci --prefix modules/cua-sdk --ignore-scripts
npm ci --prefix modules/node-repl --ignore-scripts
node modules/build.mjs
npm run build
```

本地路径依赖要求先构建cua-sdk。Windows/macOS均使用绝对Node路径启动 `modules/node-repl/dist/index.js`。worker.example.json已指向该入口。Mac SSH可能不含/usr/local/bin，需显式设置PATH。

原生驱动显式准备，不在npm安装或Agent执行动作时下载：

```text
node modules/cua-sdk/scripts/provision-native.mjs ABSOLUTE_CACHE_DIRECTORY
```

部署时为进程配置 QWEN_CUA_SDK_CACHE_DIR 指向相同目录；这是兼容原生资产加载器的配置名，不依赖Qwen CLI。驱动版本固定0.27.0，下载自官方release，按checksums.txt校验；保留NOTICE.md规定的原生运行时来源与许可证。未验证驱动可加载前不得宣称桌面测试通过。

MCP预检仍使用 `node dist/src/preflight-cli.js CONFIG REPORT`，CONFIG 的command为Node绝对路径，args为本地模块入口。READY仅代表js/js_reset协议可用。

2026-11-13：双平台模块构建成功；原生资产下载因fetch failed/timeout未完成。不得更改TLS校验或系统安全设置来解决。Mac仍需用户为实际驱动宿主授予所需系统权限，不能由测试脚本绕过。

Offline installation: node modules/cua-sdk/scripts/provision-native.mjs ABSOLUTE_CACHE_DIRECTORY ABSOLUTE_OFFICIAL_ARCHIVE

Supports official 0.27.0 Windows x64 and macOS universal archives. Preserve the official filename. SHA256 is pinned in offline-native.mjs and checked before extraction; incomplete archives fail without a completion marker. No network or desktop session is opened. Existing complete caches are reused.

Load-only verification after provisioning:

node modules/cua-sdk/scripts/check-native.mjs ABSOLUTE_CACHE_DIRECTORY NEW_REPORT_JSON

The report includes platform, Node version, native file hashes and ABI initialization result. It never creates a desktop host. PASS is not desktop permission or product acceptance. Missing or broken payloads exit nonzero. The report path must not already exist.

macOS compatibility: the pinned 0.27.0 native library declares minimum macOS 13.0 (otool LC_BUILD_VERSION). macOS 12.4 was tested and cannot load it. check-native now reports UNSUPPORTED_OS_VERSION before loading. macOS 13+ still requires an actual successful ABI check; the minimum alone is not an acceptance result.
