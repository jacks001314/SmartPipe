package releaseagent

import (
	"fmt"
	"strings"

	"smartpipe/backend/internal/releasev2"
)

// BaseInstructions 是发布 Agent 的角色约束，独立于具体运行。
const BaseInstructions = `你是 SmartPipe 的发布 Agent。你只负责把已经规划好的这一个发布运行推进到可交付状态。
规则：
1. 一切发布动作都通过 smartpipe_release MCP 工具完成；构建机上的环境修复必须走 executor_exec，不要假设你本机 shell 能访问构建机。
2. 不读取、不打印、不猜测任何令牌、密钥或凭据；MCP 鉴权已经由运行环境处理。
3. 不修改版本号、不更换构建机；不需要人工审批，按发布策略自动推进。只有遇到不可恢复的错误才停下并给出根因。
4. 每一步都以工具返回的结构化结果为准，失败要读日志、给出可执行的下一步，而不是重试到无限循环。
5. 排查构建机问题有固定顺序：先 executor_probe（含 executor 版本与安装时间，可确认执行器是否已更新），再看 build_logs 与 executor_logs，然后才做修复；不要用 grep 反推二进制实现。
6. 每次修复后立刻 build_retry 验证效果；同一个假设最多验证两次，连续三次同类修复无效就换方向，仍不行则停下报告根因，不要靠反复探测消耗时间。
7. 最后用中文给出结论：运行状态、制品摘要、已发布的渠道、失败的根因与已执行的修复动作。`

// BuildPrompt 生成交给发布 Agent 的任务描述。提示词只包含运行元数据，
// 不包含任何凭据；模型通过 MCP 工具自行读取运行详情。
func BuildPrompt(bundle *releasev2.RunBundle, mcpURL, serverName string) string {
	if bundle == nil || bundle.Run == nil {
		return ""
	}
	run := bundle.Run
	var b strings.Builder
	fmt.Fprintf(&b, "请执行发布运行 %s。\n\n", run.ID)
	b.WriteString("运行元数据：\n")
	fmt.Fprintf(&b, "- 项目：%s\n- 版本：%s", run.ProjectID, run.Version)
	if run.BuildVersion != "" {
		fmt.Fprintf(&b, "（构建版本 %s）", run.BuildVersion)
	}
	b.WriteString("\n")
	if run.Channel != "" {
		fmt.Fprintf(&b, "- 目标渠道：%s\n", run.Channel)
	}
	fmt.Fprintf(&b, "- 模式：%s\n- 仓库：%s\n- 分支：%s\n- 提交：%s\n",
		run.Mode, run.Repo, run.SourceBranch, firstNonEmpty(run.ResolvedCommit, run.SourceCommit))
	fmt.Fprintf(&b, "- 计划指纹：%s\n", run.PlanSHA256)
	if run.Status == releasev2.RunNeedsAttention {
		b.WriteString("- 当前状态：needs_attention（上一轮失败，请先读事件与构建日志定位根因，再决定修复或重试）\n")
	}
	if run.Status == releasev2.RunPartiallyPublished {
		b.WriteString("- 当前状态：partially_published（升级服务渠道指针已推进，但 GitHub 草稿/资产/发布或回读没有完成；先 release_event_list 确认失败点，补完 GitHub 后直接 release_publish 收口。不要重新灌库，也不要再次推进渠道）\n")
	}
	if len(bundle.Targets) > 0 {
		b.WriteString("\n构建目标：\n")
		for _, target := range bundle.Targets {
			fmt.Fprintf(&b, "- %s（%s/%s，构建机 %s，状态 %s）\n",
				target.TargetID, target.OS, target.Arch, firstNonEmpty(target.ExecutorID, "未分配"), target.Status)
		}
	}
	b.WriteString("\n可用工具（MCP 服务器 ")
	b.WriteString(serverName)
	b.WriteString("，地址 ")
	b.WriteString(mcpURL)
	b.WriteString("）：\n")
	b.WriteString(`- release_run_get / release_event_list：读取运行、目标、制品、事件
- executor_list：确认构建机在线与平台
- executor_probe / executor_exec / executor_logs / executor_files：探测构建机环境、执行受白名单约束的修复命令、读日志和列目录
- build_dispatch / build_wait / build_logs / build_retry：派发、等待、读日志、重试构建
- artifact_list / artifact_verify：核对制品清单与摘要
- release_notes_save：写入发布说明
- release_import：一次完成「生成升级包 + 上传到全部选中的升级服务」，多台服务并行，单台失败不影响其他台
- update_bundle_build / update_import_stage：需要单独重建升级包或只重试某几台服务时使用
- update_channel_promote：推进渠道并发布 BOM，带指针 CAS 与失败自动回滚
- github_release_sync：一次完成 GitHub 草稿、上传全部资产、发布与 digest 回读
- github_draft_create / github_asset_upload / github_release_publish / github_release_get：需要分步处理 GitHub 时使用
- release_publish：收口发布
`)
	if run.Mode == releasev2.ModePublish {
		b.WriteString(`
本次是完整发布（publish）：制品核对通过后，直接调用 github_release_sync 完成草稿、全部资产上传、发布与 digest 回读，再调用 release_publish 收口。
`)
	}
	b.WriteString(`
执行顺序：
1. 先用 release_run_get 读取当前状态，确认目标构建机与阻断项。
2. 需要构建时按目标逐个 build_dispatch，然后 build_wait 等待结果；失败就读 build_logs。若日志显示缺工具、磁盘满、证书或路径等环境问题，先用 executor_probe 确认现状，再用 executor_exec 修复（安装依赖、清理缓存等），修复后 build_retry 并说明原因。Electron 全量构建常常要几十分钟，build_wait 返回“仍在构建”时不要连续干等：把已完成的诊断/修复和剩余步骤写进最终结论后结束本轮，运行会在目标全部就绪后自动再次交回（publish）或直接收口为 built（build_only）。
3. 构建完成后用 artifact_list 与 artifact_verify 核对制品，再 release_notes_save 写入发布说明。
4. 直接调用 release_import（会生成升级包并把每个制品上传到全部选中的升级服务，不移动渠道指针）。返回 state=partial 时只对 retry_services 里的服务再次调用 release_import 并传 services=[...]，不要重传成功的服务。制品以「槽位 + 平台 + 架构」为键、摘要一致就跳过，所以这次调用超时或中断后，用同样的参数再调一次即可从断点继续，不会重传已完成的制品。若返回 version_already_published，说明这个版本在升级服务上已经存在且制品摘要不同：不要重试、不要加 replace_existing，直接把「需要提升产品版本号后再发」写进最终结论并结束本轮——同版本换字节已装客户端不会升级。
5. 不需要审批，但顺序不能颠倒：任一制品的 destinations 含 github 时，先用 github_release_sync 完成 GitHub 草稿、资产上传与发布回读；再调用 release_publish 收口（它会推进渠道、校验 BOM 并回读）。release_publish 是最后一步，先调它会因为没有 GitHub Release 而落到 partially_published。CAS 期望值可以不传：服务端在推进前会自己读当前渠道指针并做二次校验，推进后回读 BOM；校验失败会自动回滚到上一版并把该服务记入隔离表。partial 时只重试失败服务，最后再次 release_run_get 确认最终状态。
6. 任何阶段失败都先读日志、尝试修复或重试；无法恢复才停止，并把根因、已执行的修复、隔离/回滚结果和建议写进最终结论。
`)
	return b.String()
}
