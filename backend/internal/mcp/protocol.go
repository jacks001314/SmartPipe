package mcp

import (
	"encoding/json"
)

// Tool 是一个可被 Agent 调用的工具定义。
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	// Roles 为空表示所有角色可用。
	Roles []string `json:"-"`
}

func obj(props map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
func strs(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

// Tools 是网关暴露的完整工具集；实际可见集合按角色过滤。
var Tools = []Tool{
	{
		Name:        "project.info",
		Description: "读取项目配置（仓库地址、默认分支、标签前缀、蓝信群绑定），不包含任何凭据。",
		InputSchema: obj(map[string]any{"project_id": str("项目 ID；省略时使用令牌绑定的项目")}),
	},
	{
		Name:        "issue.get",
		Description: "读取 GitHub Issue 的标题、状态与链接。",
		InputSchema: obj(map[string]any{"number": num("Issue 编号"), "project_id": str("项目 ID（可选）")}, "number"),
		Roles:       []string{"lead"},
	},
	{
		Name:        "issue.comment",
		Description: "在 GitHub Issue 下发评论（方案讨论、进度同步）。评论内容会写入审计。",
		InputSchema: obj(map[string]any{"number": num("Issue 编号"), "body": str("Markdown 评论内容"), "project_id": str("项目 ID（可选）")}, "number", "body"),
		Roles:       []string{"lead"},
	},
	{
		Name:        "issue.add_labels",
		Description: "给 Issue 追加标签。发布候选请使用项目的 ready 标签（默认 smartpipe:ready）。",
		InputSchema: obj(map[string]any{"number": num("Issue 编号"), "labels": strs("标签列表"), "project_id": str("项目 ID（可选）")}, "number", "labels"),
		Roles:       []string{"lead"},
	},
	{
		Name:        "release.candidates",
		Description: "列出当前 ready 标签下的候选 Issue（用于确认本次发版范围）。",
		InputSchema: obj(map[string]any{"project_id": str("项目 ID（可选）")}),
		Roles:       []string{"lead", "release"},
	},
	{
		Name:        "repo.commit",
		Description: "由 lead 在干净副本上应用补丁、创建 agent/<issue> 分支并提交（可选合并到目标分支）。复杂 Issue 未批准会返回 approval_required。",
		InputSchema: obj(map[string]any{
			"issue_ref":     str("Issue 引用，如 owner/repo#123"),
			"repo":          str("仓库地址或本地路径"),
			"base_commit":   str("基线 commit"),
			"patch":         str("unified diff"),
			"branch":        str("目标分支名；省略时按 issue 编号生成 agent/<n>"),
			"message":       str("提交信息"),
			"target_branch": str("需要同时合并时填目标分支（如 main）"),
			"author_name":   str("提交人姓名（可选）"),
			"author_email":  str("提交人邮箱（可选）"),
			"project_id":    str("项目 ID（可选）"),
		}, "repo", "base_commit", "patch"),
		Roles: []string{"lead"},
	},
	{
		Name:        "repo.push",
		Description: "推送已提交的分支到项目远端。远端地址与 Git 凭据由服务端注入，Agent 不接触 token；禁止 force push。",
		InputSchema: obj(map[string]any{
			"issue_ref":  str("Issue 引用（用于回写评论）"),
			"repo":       str("本地工作副本路径"),
			"branch":     str("要推送的分支"),
			"remote":     str("远端名，默认 origin"),
			"project_id": str("项目 ID（可选）"),
		}, "repo", "branch"),
		Roles: []string{"lead"},
	}, {
		Name:        "verify.patch",
		Description: "在干净副本上应用补丁并执行 smartpipe.yaml 的 required 命令；复杂 Issue 未批准会直接返回 approval_required。",
		InputSchema: obj(map[string]any{
			"issue_ref":   str("owner/repo#123 或 #123"),
			"repo":        str("仓库地址或本地路径"),
			"base_commit": str("补丁的基线 commit"),
			"patch":       str("unified diff"),
			"test_log":    str("dev 自测日志（可选）"),
			"author":      str("提交人（可选）"),
			"project_id":  str("项目 ID（可选）"),
		}, "issue_ref", "repo", "base_commit", "patch"),
		Roles: []string{"lead"},
	},
	{
		Name:        "derivation.create",
		Description: "派生一个子 Agent（dev/test/release）。服务端会先校验复杂 Issue 的批准状态，未批准返回 approval_required。",
		InputSchema: obj(map[string]any{
			"issue_ref":     str("Issue 引用"),
			"agent_name":    str("子 Agent 名称"),
			"display_name":  str("展示名（可选）"),
			"template_name": str("Agent 模板（可选）"),
			"project_id":    str("项目 ID（可选）"),
		}, "issue_ref", "agent_name"),
		Roles: []string{"lead"},
	},
	{
		Name:        "release.run",
		Description: "执行发版：读取 ready 候选、干净构建、创建 tag 与 GitHub Release、回写标签、通知蓝信。dry_run=true 只构建。",
		InputSchema: obj(map[string]any{
			"version":        str("版本号，例如 v2026.11.05"),
			"commit":         str("构建 commit（可选，默认分支 HEAD）"),
			"branch":         str("目标分支（可选）"),
			"notes":          str("发布说明（可选）"),
			"dry_run":        map[string]any{"type": "boolean", "description": "只构建，不发布"},
			"artifact_globs": strs("制品 glob（可选）"),
			"project_id":     str("项目 ID（可选）"),
		}, "version"),
		Roles: []string{"lead", "release"},
	},
	{
		Name:        "release.rollback",
		Description: "回滚一次已发布版本：删除 GitHub Release 与 tag，候选 Issue 退回 ready 并通知蓝信。",
		InputSchema: obj(map[string]any{"run_id": num("发版运行 ID"), "project_id": str("项目 ID（可选）")}, "run_id"),
		Roles:       []string{"lead", "release"},
	},
}

// ToolsFor 返回指定角色可见的工具。
func ToolsFor(role string) []Tool {
	out := make([]Tool, 0, len(Tools))
	for _, t := range Tools {
		if len(t.Roles) == 0 {
			out = append(out, t)
			continue
		}
		for _, r := range t.Roles {
			if r == role {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

// ToolAllowed 判断角色是否可以使用某个工具。
func ToolAllowed(role, name string) bool {
	for _, t := range Tools {
		if t.Name != name {
			continue
		}
		if len(t.Roles) == 0 {
			return true
		}
		for _, r := range t.Roles {
			if r == role {
				return true
			}
		}
		return false
	}
	return false
}

// Arguments 解析工具参数。
func Arguments(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, nil
}
