# Runner 运行时预检

先运行 `npm ci` 和 `npm test`。预检命令：

```text
node dist/src/preflight-cli.js CONFIG_JSON REPORT_JSON
```

配置为 `{ "command": "绝对路径/node.exe", "args": ["绝对路径/node-repl-mcp/dist/index.js"] }`。通过进程参数执行，不使用 shell。报告文件必须不存在，避免覆盖既有证据。

只连接预安装 MCP 并发现 js/js_reset，不运行桌面脚本；READY 只代表协议准备就绪。预检设初始化/发现超时，不将机器 API token 传入子进程，也不输出上游 stderr 或连接错误正文。

2026-11-13 Windows Node24.15.0 实测：隔离安装 @qwen-code/node-repl-mcp@0.27.0（ignore-scripts）后，真实 MCP 初始化成功，发现 js/js_reset，报告 READY。尚未证明该包的原生 CUA 驱动可用，不能替代截图/点击、Worker 服务端联调和产品验收。

Runner 测试18项全部通过，包括初始化挂起、非Qwen服务和相对路径拒绝。实际结果位于 reports/online-test/qwen-preflight.json。
