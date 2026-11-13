# 自有桌面模块验收状态

已验证：Windows/Mac 独立源码构建；CUA模拟驱动26项通过；Node REPL Windows169通过13跳过、Mac181通过1跳过；本地MCP协议初始化；Runner20项回归；真实TS客户端→Go HTTP→PostgreSQL18场景夹具链路。

尚未验证：原生驱动加载/权限与真实桌面输入，完整Syntropy候选制品安装启动升级，发布门禁真实阻断/放行，PG17。

Linux的SSH可用但Docker socket仍拒绝，PG17未安装；仅PG18测试通过。原生Windows/Mac包下载持续低速，完整SHA256验证前不安装。候选Syntropy版本正在向用户确认。

这些模块是从固定上游源码提取的SmartPipe私有包，不包含Qwen CLI/core/模型账号系统。保留上游LICENSE/NOTICE与UPSTREAM.json；原生运行时仍需按许可证提供相应来源信息。不得把本文件中的模块回归通过当作产品发布批准。
