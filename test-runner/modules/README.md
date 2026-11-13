# SmartPipe 自有桌面模块

用户明确要求只搬入需要的模块，不使用整体 Qwen Code。

第一步已提取 node-repl 源码与测试、构建文件、Apache-2.0 LICENSE；来源提交和原文件 SHA256 记录在 node-repl/UPSTREAM.json。包重命名为 @smartpipe/node-repl，删除 desktop-relay CLI 分支，不导入 Qwen CLI、core、模型或账号系统。

此为源码迁入阶段，尚未完成独立构建和运行验证。Node REPL 暂仍引用独立 @qwen-code/cua-sdk；下一步需迁入所需 CUA 层并切断其 browser-use 依赖，保留截图、窗口/控件读取和输入。原生 Windows/macOS 驱动仍需核验二进制与许可证。

此前 .work/qwen-runtime 和 Mac /tmp/smartpipe-qwen-027 仅用于上游独立模块兼容性验证，不作为最终 SmartPipe 交付依赖；从未安装 Qwen Code CLI。不要将源码提取完成视为整体验收完成。
