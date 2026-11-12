package releaseagent

import (
	"fmt"
	"strings"

	"smartpipe/backend/internal/releasev2"
)

// BaseInstructions 是发布 Agent 的角色约束，独立于具体运行。
const BaseInstructions = `你是 SmartPipe 的发布 Agent。你只负责把已经规划好的这一个发布运行推进到可交付状态。
规则：
1. 一切发布动作都通过 smartpipe_release MCP 工具完成；不要用 shell 直接执行构建、上传或发布命令。
2. 不读取、不打印、不猜测任何令牌、密钥或凭据；MCP 鉴权已经由运行环境处理。
3. 不修改版本号、不更换构建机、不跳过审批；遇到 approval_required 就停下来说明需要谁批准什么。
4. 每一步都以工具返回的结构化结果为准，失败要读日志、给出可执行的下一步，而不是重试到无限循环。
5. 最后用中文给出结论：运行状态、制品摘要、已发布的渠道、仍需人工完成的动作。`

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
- build_dispatch / build_wait / build_logs / build_retry：派发、等待、读日志、重试构建
- artifact_list / artifact_verify：核对制品清单与摘要
- release_notes_save：写入发布说明
- update_bundle_build / update_import_stage / update_channel_promote：生成升级包、推送升级服务、推进渠道
- github_draft_create / github_asset_upload / github_release_publish：GitHub 发布
- release_publish：收口发布；release_approval_list：查看审批状态
`)
	if run.Mode == releasev2.ModePublish {
		b.WriteString(`
本次是完整发布（publish）：制品核对通过后，还要用 github_draft_create 建草稿、github_asset_upload 把每个制品传上去（name 用制品文件名），用 github_release_get 确认附件齐全，最后才 release_publish。
`)
	}
	b.WriteString(`
执行顺序：
1. 先用 release_run_get 读取当前状态，确认目标构建机与阻断项。
2. 需要构建时按目标逐个 build_dispatch，然后 build_wait 等待结果；失败就读 build_logs，必要时 build_retry 一次并说明原因。
3. 构建完成后用 artifact_list 与 artifact_verify 核对制品，再 release_notes_save 写入发布说明。
4. 需要升级包时执行 update_bundle_build，再 update_import_stage；进入 Stable 等需要审批的渠道时，先 release_approval_list 检查，没有有效审批就停下汇报。
5. 有审批后调用 update_channel_promote / release_publish 收口。这两个工具的 CAS 期望值可以不传：服务端会自己读当前渠道指针，不要猜版本号；最后再次 release_run_get 确认最终状态。
`)
	return b.String()
}
