# Windows 验证进展

2026-11-13：修改 mergeapi_test.go，以临时真实 Git 仓库验证本地提交、main 内容、repo_ops 审计；用本地 bare remote 验证 push，并独立检查服务端假 token 注入。未外连 GitHub。modelconfig_import_test.go 在缺文件场景隔离 HOME/USERPROFILE，增加无数据库依赖的候选路径回归。

实际命令：go test ./internal/adminapi -run 'Test(LocalConfigCandidates|CommitLocally|PushInjects|ModelConfigLocalPreview)' -count=1 -v：隔离回归通过，三个 DB 测试因缺 SMARTPIPE_TEST_DSN 跳过。go test ./internal/patch -count=1 成功。不能认定此前两个 DB 失败已复测通过。

通过当前平台 computer 工具在真实 Windows 读取 SmartPipe Chrome 窗口：187 个控件，截图返回成功（1936x1048）。点击“事件时间线”Tab 后重新读取，selectedElements 确认其成为选中项。该结果证明当前平台 Windows 截图/控件读取/点击能力，不代表新 test-runner 与独立 Qwen MCP 的端到端验收。

现有在线管理台导航尚未显示新增在线测试页面；未部署候选版本。Linux SSH 认证仍未配置，未重建已被用户要求移除的 Agent。后续需要独立数据库复测与候选 Runner 部署。
