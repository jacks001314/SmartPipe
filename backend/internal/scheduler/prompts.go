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
- 你不需要直接建单或派发 worker：输出 issue 后，调度器会直接创建 Issue 并排队 fix worker，群里只收到一行摘要；issue_update 会直接补充已有 Issue。不要在回复中声称自己不能建单或派发 worker；
- 群内 /status、/pause、/resume、/retry、/stop、/fix 由调度器直接执行；用户问怎么控制进度时，直接给出这些命令；
- 当上下文给出“Issue #N 调度状态”时，以它为准回答“有没有派发 worker / 修复进度 / PR 状态”；没有状态信息时，让用户带上 #编号，不要猜测；
- 不要臆测代码行为，结论要基于实际读到的代码；不确定就说明不确定；
- 不要新建重复 Issue：系统会按标题和错误特征查重；
- 群回复只给结论：2 行以内、不超过 80 字；复现步骤、日志、代码细节写进 Issue 正文，不要发到群里。

输出必须是**一个 JSON 对象**，不要加 markdown 代码块，不要输出多余文字：
{"action":"answer","reply":"给群里的回复"}
{"action":"issue","title":"不超过 60 字","body":"现象/复现/影响/期望","severity":"low|medium|high|critical"}
{"action":"need_info","reply":"需要补充什么信息"}
{"action":"issue_update","issue":123,"reply":"要追加到该 Issue 的评论"}`

// ObserveSystemPrompt 用于非 @ 群消息的轻量观察。默认沉默，只有明确指向机器人、
// 项目或当前 Issue，或者描述具体缺陷/需求时才回答或 triage。
const ObserveSystemPrompt = `你是 SmartPipe 蓝信群的观察者，负责判断一条没有 @ 机器人的群消息是否需要介入。

默认输出 silent。只有满足下面任一条件时才不要沉默：
1. 消息明确在问机器人、SmartPipe 项目、当前 Issue、worker 或 PR；
2. 消息描述了具体、可复现的产品缺陷或明确需求；
3. 消息是在补充正在讨论的 Issue，或追问修复进度；
4. 群里有人卡住、说“怎么弄/为啥这样”这类需要一句话就能点醒的问题。

规则：
- 不要回答团队内部闲聊、打招呼、与项目无关的讨论；
- 不要猜测代码行为；不确定就 silent；
- 即使回答，也要短（3 行以内）；
- triage 只用于确实值得进入建单/补 Issue 流程的内容，并给出 0 到 1 的 confidence；
- confidence 必须反映真实把握，不要一律写 1。

只输出一个 JSON 对象，不要 markdown，不要多余文字：
{"action":"silent","reason":"为什么沉默","confidence":0.2}
{"action":"answer","reply":"给群里的回复","reason":"为什么回答","confidence":0.7}
{"action":"triage","issue":123,"reply":"追加到 Issue 的补充","reason":"为什么建单","confidence":0.9}
{"action":"triage","title":"不超过 60 字","body":"现象/复现/影响/期望","severity":"low|medium|high|critical","reason":"为什么建单","confidence":0.9}`

// TriageDecision 是观察步骤的结构化输出。
type TriageDecision struct {
	Action     string  `json:"action"`
	Reply      string  `json:"reply"`
	Title      string  `json:"title"`
	Body       string  `json:"body"`
	Severity   string  `json:"severity"`
	Issue      int     `json:"issue"`
	Reason     string  `json:"reason"`
	Confidence float64 `json:"confidence"`
}

func ParseTriageDecision(raw string) (*TriageDecision, error) {
	text := strings.TrimSpace(raw)
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("scheduler: no json object in triage decision")
	}
	var d TriageDecision
	if err := json.Unmarshal([]byte(text[start:end+1]), &d); err != nil {
		return nil, fmt.Errorf("scheduler: parse triage decision: %w", err)
	}
	d.Action = strings.ToLower(strings.TrimSpace(d.Action))
	switch d.Action {
	case "silent", "answer", "triage":
	default:
		return nil, fmt.Errorf("scheduler: unknown triage action %q", d.Action)
	}
	if d.Confidence < 0 {
		d.Confidence = 0
	}
	if d.Confidence > 1 {
		d.Confidence = 1
	}
	if d.Action == "answer" && strings.TrimSpace(d.Reply) == "" {
		return nil, fmt.Errorf("scheduler: answer action requires reply")
	}
	if d.Action == "triage" {
		d.Title = strings.TrimSpace(d.Title)
		d.Reply = strings.TrimSpace(d.Reply)
		if d.Issue <= 0 && d.Title == "" {
			return nil, fmt.Errorf("scheduler: triage action requires issue or title")
		}
		if d.Issue <= 0 {
			if d.Body == "" {
				d.Body = d.Title
			}
			if d.Severity == "" {
				d.Severity = "medium"
			}
		}
	}
	return &d, nil
}

// FixSystemPrompt 是 fix worker 的系统提示词：只在自己的 worktree 里改代码。
const FixSystemPrompt = `你是 SmartPipe 的修复工程师（fix worker），只负责一个 GitHub Issue。

你所在目录是这个 Issue 专属的 git worktree。你的职责：
1. 通读 Issue 正文和最近评论，确认要解决的问题与验收标准；
2. 在代码里定位根因，做最小、可维护的修改；
3. 执行仓库 smartpipe.yaml 中 required_for_merge 列出的命令（至少 build），再补跑与改动相关的测试；
4. 用中文写一段小结：改了什么、跑了哪些命令、各自结果、还有什么风险或待确认项。

硬性约束：
- 只在当前工作目录内修改文件；不要访问其他 worktree；
- 不要执行 git commit / git push / 创建 PR——提交由服务端门禁完成；
- 不要修改 .git、不要读写任何凭据、不要联网下载依赖；
- 不要为了通过测试而删除或跳过测试；
- 如果需求不明确，先给出方案和需要确认的问题，不要盲目改代码；
- 必须实际执行仓库 smartpipe.yaml 里定义的验证命令，不能只说“已测试”；
- 提交由调度器完成，你只需要把文件改好并贴出命令输出。

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

func buildChatPrompt(p *Project, history []ChatMessage, msg Inbound, referenced int, activeWorker *Worker) string {
	var b strings.Builder
	fmt.Fprintf(&b, "项目：%s（仓库 %s，默认分支 %s）\n", p.Name, p.Repo, p.DefaultBase)
	if p.LocalPath != "" {
		fmt.Fprintf(&b, "本地代码目录：%s（只读）\n", p.LocalPath)
	}
	if referenced > 0 {
		fmt.Fprintf(&b, "这条消息引用了 Issue #%d，请优先按 issue_update 处理。\n", referenced)
		fmt.Fprintf(&b, "调度器状态：%s\n", workerPromptStatus(referenced, activeWorker))
	}
	if len(history) > 0 {
		b.WriteString("\n最近会话：\n")
		for _, h := range history {
			role := "同事"
			switch h.Role {
			case "assistant":
				role = "你"
			case "observer":
				role = "系统判断"
			}
			fmt.Fprintf(&b, "- %s：%s\n", role, clamp(h.Body, 600))
		}
	}
	text := clamp(msg.Text, 4000)
	if strings.TrimSpace(text) == "" {
		text = "（这条消息只带了附件，没有文字说明，请按附件内容理解意图）"
	}
	fmt.Fprintf(&b, "\n群：%s\n发言人：%s\n消息：%s\n\n请按系统提示词输出一个 JSON 对象。",
		firstNonEmpty(msg.GroupName, "未知群"), firstNonEmpty(msg.SenderName, "同事"), text)
	return b.String()
}

func buildObservePrompt(p *Project, history []ChatMessage, msg Inbound) string {
	var b strings.Builder
	fmt.Fprintf(&b, "项目：%s（仓库 %s）\n", p.Name, p.Repo)
	if len(history) > 0 {
		b.WriteString("\n最近群聊：\n")
		for _, h := range history {
			role := "同事"
			switch h.Role {
			case "assistant":
				role = "机器人"
			case "observer":
				role = "系统判断"
			}
			fmt.Fprintf(&b, "- %s：%s\n", role, clamp(h.Body, 500))
		}
	}
	text := clamp(msg.Text, 4000)
	if strings.TrimSpace(text) == "" {
		text = "（只有附件，没有文字）"
	}
	fmt.Fprintf(&b, "\n群：%s\n发言人：%s\n未 @ 机器人的消息：%s\n\n请判断是否应该 silent、answer 或 triage，并输出 JSON。",
		firstNonEmpty(msg.GroupName, "未知群"), firstNonEmpty(msg.SenderName, "同事"), text)
	return b.String()
}

// workerPromptStatus 把调度器已知的 worker 状态交给聊天模型，避免模型凭感觉回答“没有派发”。
func workerPromptStatus(issue int, w *Worker) string {
	if w == nil {
		return fmt.Sprintf("Issue #%d 当前没有进行中的 worker", issue)
	}
	parts := []string{fmt.Sprintf("Issue #%d 当前 worker 状态=%s", issue, workerStateLabel(w.State))}
	if w.Branch != "" {
		parts = append(parts, "分支="+w.Branch)
	}
	if w.PRNumber > 0 {
		parts = append(parts, fmt.Sprintf("PR=#%d", w.PRNumber))
	}
	if w.BlockedReason != "" {
		parts = append(parts, "阻塞原因="+clamp(w.BlockedReason, 200))
	}
	if w.LastError != "" {
		parts = append(parts, "最近错误="+clamp(w.LastError, 200))
	}
	return strings.Join(parts, "，")
}

func buildIssueReply(msg Inbound, issue Issue) string {
	return fmt.Sprintf("已创建 Issue #%d：%s\n%s\n来源：%s", issue.Number, issue.Title, issue.URL, firstNonEmpty(msg.SenderName, "同事"))
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

// groupMessageLimit 是发到蓝信群的单条消息上限：群里只要结论，细节留在 Issue。
const groupMessageLimit = 400

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
