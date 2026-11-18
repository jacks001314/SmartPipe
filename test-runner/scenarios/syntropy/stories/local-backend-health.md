# 故事：Syntropy 本地后端可用性

## 背景
Syntropy 桌面界面依赖本机 local master / gateway（安装版默认 127.0.0.1:3333）。
界面出现不代表产品可用；本故事只验证本地后端链路。

## 步骤
1. 调用 `env_processes`，记录所有名称或命令行包含 Syntropy 的进程。
2. 检查 `127.0.0.1:3333` 是否监听：`Test-NetConnection` 或等价方式。
3. 若监听，调用 `env_http GET http://127.0.0.1:3333/`，记录状态码；非 2xx 也要如实记录。
4. 若未监听，检查安装目录下的 `resources\bin\win32-x64\syntropy-master.exe` 是否存在，并记录结论。
5. 截图当前界面，作为 `backend_state` 的证据。
6. 不启动、不停止、不修改任何 Syntropy 组件；本故事是只读诊断。

## 验收检查
- `backend_port`：3333 监听状态与 HTTP 结果（证据：JSON）
- `backend_state`：截图（证据：PNG）

## 判定
- 端口监听且 HTTP 非 5xx：PASS。
- 端口未监听或持续 5xx：FAIL，并在 summary 中写出可复现的现象。
- 因权限或环境限制无法判断：BLOCKED，不要猜测。
