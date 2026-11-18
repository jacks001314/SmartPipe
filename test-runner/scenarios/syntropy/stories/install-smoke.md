# 故事：Syntropy 安装完好性与主界面冒烟

## 背景
测试机已安装 Syntropy 桌面客户端。本次会话验证安装产物可用，且真实桌面能正常启动并显示主界面。

## 输入
- `install_root`：Syntropy 安装目录，必须由会话创建者提供。

## 步骤
1. 读取 `install_root` 下的文件清单，确认 `Syntropy.exe` 与 `resources\bin\win32-x64\syntropy-app-server.exe` 都存在。
2. 记录安装版本：读取 resources 下可获得的版本信息；无法读取时记录为 BLOCKED，不要猜测。
3. 启动 `Syntropy.exe`（绝对路径）。
4. 轮询进程与主窗口，最多 120 秒；必须观察到 MainWindowTitle 包含 Syntropy 的窗口。
5. 对主窗口截图，把 evidence 引用写入 `app_window` 与 `screenshot` 检查。
6. 检查后保持应用运行；不要删除用户数据。

## 验收检查
- `app_installed`：安装目录、主程序和 app-server 均存在（证据：文件清单 JSON）
- `app_window`：主窗口出现（证据：进程/窗口 JSON）
- `screenshot`：主界面截图（证据：PNG）

## 禁止
- 不要修改安装目录内文件。
- 不要结束非 Syntropy 进程。
