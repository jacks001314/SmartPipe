package scheduler

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ChatSystemPrompt 是 chat worker 的系统提示词：只读、负责问答与建单判断。
// 它和 fix worker 的提示词刻意分开——两种角色的权限、输出格式和失败处理都不同。
const ChatSystemPrompt = `你是 SmartPipe 的蓝信助手（chat worker），服务于研发群里的同事。

你的职责：
1. 理解群消息，回答产品使用、代码实现、排障类问题；可以用只读工具查看当前项目代码；
2. 判断消息是否描述了一个真实的产品缺陷或明确需求；
3. 如果是缺陷，整理成可执行的 Issue（标题、现象、复现步骤、影响范围、严重级别）；
4. 如果消息是在讨论某个已有 Issue，把内容转成该 Issue 的补充说明。

硬性约束：
- 你没有写权限：不得修改文件、不得执行会产生副作用的命令、不得提交或推送；
- 不要臆测代码行为，结论要基于实际读到的代码；不确定就说明不确定；
- 不要新建重复 Issue：系统会按标题和错误特征查重；
- 群回复要简短（3 行以内），细节写进 Issue。

输出必须是**一个 JSON 对象**，不要加 markdown 代码块，不要输出多余文字：
{"action":"answer","reply":"给群里的回复"}
{"action":"issue","title":"不超过 60 字","body":"现象/复现/影响/期望","severity":"low|medium|high|critical"}
{"action":"need_info","reply":"需要补充什么信息"}
{"action":"issue_update","issue":123,"reply":"要追加到该 Issue 的评论"}`

// FixSystemPrompt 是 fix worker 的系统提示词：只在自己的 worktree 里改代码。
const FixSystemPrompt = `你是 SmartPipe 的修复工程师（fix worker），只负责一个 GitHub Issue。

你所在目录是这个 Issue 专属的 git worktree。你的职责：
1. 通读 Issue 正文和最近评论，确认要解决的问题与验收标准；
2. 在代码里定位根因，做最小、可维护的修改；
3. 运行相关测试自测；
4. 用中文写一段小结：改了什么、怎么验证的、还有什么风险或待确认项。

硬性约束：
- 只在当前工作目录内修改文件；不要访问其他 worktree；
- 不要执行 git commit / git push / 创建 PR——提交由服务端门禁完成；
- 不要修改 .git、不要读写任何凭据、不要联网下载依赖；
- 不要为了通过测试而删除或跳过测试；
- 如果需求不明确，先给出方案和需要确认的问题，不要盲目改代码；
- 必须遵守仓库 smartpipe.yaml 里定义的验证命令。

完成一轮后直接输出小结（不要 JSON，不要 markdown 标题）。`

// ChatDecision 是 chat worker 的结构化输出。
type ChatDecision struct {
	Action   string `json:"action"`
	Reply    string `json:"reply"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Severity string `json:"severity"`
	Issue    int    `json:"issue"`
}

// ParseChatDecision 从模型输出里提取 JSON；允许前后有解释文字或代码块围栏。
func ParseChatDecision(raw string) (*ChatDecision, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil, fmt.Errorf("scheduler: empty chat decision")
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("scheduler: no json object in chat decision")
	}
	var d ChatDecision
	if err := json.Unmarshal([]byte(text[start:end+1]), &d); err != nil {
		return nil, fmt.Errorf("scheduler: parse chat decision: %w", err)
	}
	switch d.Action {
	case "answer", "issue", "need_info", "issue_update":
	default:
		return nil, fmt.Errorf("scheduler: unknown chat action %q", d.Action)
	}
	if d.Action == "issue" {
		d.Title = strings.TrimSpace(d.Title)
		if d.Title == "" {
			return nil, fmt.Errorf("scheduler: issue action requires title")
		}
		if d.Body == "" {
			d.Body = d.Title
		}
		if d.Severity == "" {
			d.Severity = "medium"
		}
	}
	if d.Action == "issue_update" && d.Issue <= 0 {
		return nil, fmt.Errorf("scheduler: issue_update requires issue number")
	}
	return &d, nil
}

func buildChatPrompt(p *Project, history []ChatMessage, msg Inbound, referenced int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "项目：%s（仓库 %s，默认分支 %s）\n", p.Name, p.Repo, p.DefaultBase)
	if p.LocalPath != "" {
		fmt.Fprintf(&b, "本地代码目录：%s（只读）\n", p.LocalPath)
	}
	if referenced > 0 {
		fmt.Fprintf(&b, "这条消息引用了 Issue #%d，请优先按 issue_update 处理。\n", referenced)
	}
	if len(history) > 0 {
		b.WriteString("\n最近会话：\n")
		for _, h := range history {
			role := "同事"
			if h.Role == "assistant" {
				role = "你"
			}
			fmt.Fprintf(&b, "- %s：%s\n", role, clamp(h.Body, 600))
		}
	}
	text := clamp(msg.Text, 4000)
	if strings.TrimSpace(text) == "" {
		text = "（这条消息只带了附件，没有文字说明，请按附件内容理解意图）"
	}
	fmt.Fprintf(&b, "\n群：%s\n发言人：%s\n消息：%s\n\n请按系统提示词输出一个 JSON 对象。",
		msg.GroupName, msg.SenderName, text)
	return b.String()
}

func buildIssueReply(msg Inbound, issue Issue) string {
	return fmt.Sprintf("已创建 Issue #%d：%s\n%s\n来源：%s", issue.Number, issue.Title, issue.URL, msg.SenderName)
}

func buildFixPrompt(p *Project, issue Issue, comments []Comment, trigger string, prevFailure string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "项目：%s（仓库 %s，基线分支 %s）\n", p.Name, p.Repo, p.DefaultBase)
	fmt.Fprintf(&b, "Issue #%d：%s\n状态：%s\n\n", issue.Number, issue.Title, issue.State)
	b.WriteString("Issue 正文：\n")
	b.WriteString(clamp(issue.Body, 8000))
	b.WriteString("\n")
	if len(comments) > 0 {
		b.WriteString("\n最近评论（按时间顺序）：\n")
		start := 0
		if len(comments) > 12 {
			start = len(comments) - 12
		}
		for _, c := range comments[start:] {
			fmt.Fprintf(&b, "- %s：%s\n", firstNonEmpty(c.Author, "同事"), clamp(c.Body, 1200))
		}
	}
	if strings.TrimSpace(trigger) != "" {
		b.WriteString("\n本轮新增的人工输入：\n")
		b.WriteString(clamp(trigger, 6000))
		b.WriteString("\n")
	}
	if strings.TrimSpace(prevFailure) != "" {
		b.WriteString("\n上一轮验证没有通过，原因如下（请先修复它）：\n")
		b.WriteString(clamp(prevFailure, 4000))
		b.WriteString("\n")
	}
	b.WriteString("\n请在这个 worktree 里完成修改并自测。")
	return b.String()
}

func clamp(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
