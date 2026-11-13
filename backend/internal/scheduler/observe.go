package scheduler

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode"

	"smartpipe/backend/internal/codexrunner"
)

const (
	defaultHourlyReplyLimit = 6
	defaultReplyCooldown    = 30 * time.Second
	defaultAutoIssueLimit   = 3
	defaultAnswerConfidence = 0.55
	defaultTriageConfidence = 0.85
	messageClaimTTL         = 30 * time.Minute
)

func normalizeChatPolicy(p ChatPolicy) ChatPolicy {
	switch strings.ToLower(strings.TrimSpace(p.Mode)) {
	case "smart":
		p.Mode = "smart"
	case "all":
		p.Mode = "all"
	default:
		p.Mode = "at_only"
	}
	if p.ReplyCooldown <= 0 {
		p.ReplyCooldown = defaultReplyCooldown
	}
	if p.HourlyReplyLimit <= 0 {
		p.HourlyReplyLimit = defaultHourlyReplyLimit
	}
	if p.DailyAutoIssueLimit <= 0 {
		p.DailyAutoIssueLimit = defaultAutoIssueLimit
	}
	if p.AutoIssueConfidence <= 0 || p.AutoIssueConfidence > 1 {
		p.AutoIssueConfidence = defaultTriageConfidence
	}
	if p.AnswerConfidence <= 0 || p.AnswerConfidence > 1 {
		p.AnswerConfidence = defaultAnswerConfidence
	}
	return p
}

// isExplicitCommand 只认明确的命令入口，避免普通讨论里的“暂停/继续”触发副作用。
func isExplicitCommand(text string) bool {
	t := strings.TrimSpace(text)
	for strings.HasPrefix(t, "@") {
		fields := strings.Fields(t)
		if len(fields) == 0 {
			return false
		}
		t = strings.TrimSpace(strings.TrimPrefix(t, fields[0]))
	}
	t = strings.TrimSpace(t)
	if strings.HasPrefix(t, "/") {
		return true
	}
	return strings.HasPrefix(t, "确认建单") || strings.HasPrefix(t, "取消建单") || strings.HasPrefix(t, "撤回建单") || strings.HasPrefix(t, "撤销建单")
}

func isDirectMessage(msg Inbound) bool {
	return msg.ChatType == "p2p" || msg.IsAtMe || msg.ReplyToBot || isExplicitCommand(msg.Text)
}

// isNaturalControlCommand 只拦截明显在命令机器人的短句，避免把“暂停按钮没反应”
// 这类真实缺陷误当成 worker 控制。
func isNaturalControlCommand(text string) bool {
	action, explicit := parseWorkerControl(text)
	if action == "" || explicit {
		return false
	}
	if referencedIssue(text) > 0 || strings.Contains(strings.ToLower(text), "worker") {
		return true
	}
	for _, bugWord := range []string{"没反应", "无法", "不能", "报错", "失败", "异常", "按钮", "页面", "功能", "问题"} {
		if strings.Contains(text, bugWord) {
			return false
		}
	}
	for _, word := range []string{"暂停", "恢复", "继续", "重试", "重新跑", "停止", "停掉", "终止", "重新派发", "重新排队"} {
		if strings.Contains(text, word) && len([]rune(strings.TrimSpace(text))) <= 12 {
			return true
		}
	}
	return false
}

// shouldObserve 只过滤确定没有信息量的消息（单字、纯表情、纯符号）。
// 其余消息一律交给观察模型判断要不要回应、建单还是沉默——判断权在模型，
// 关键词只做记录，不用来提前丢弃消息。
func shouldObserve(text string) bool {
	t := strings.TrimSpace(text)
	if len([]rune(t)) < 4 {
		return false
	}
	for _, r := range t {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func (s *Scheduler) allowAutoReply(ctx context.Context, p *Project, chatID string) (bool, string) {
	policy := normalizeChatPolicy(p.Policy)
	now := s.now()
	count, last, err := s.repo.RecentChatReplies(ctx, chatID, now.Add(-time.Hour))
	if err != nil {
		log.Printf("[scheduler] recent chat replies %s: %v", chatID, err)
		return true, ""
	}
	if count >= policy.HourlyReplyLimit {
		return false, fmt.Sprintf("本群最近一小时已回复 %d 条，达到上限 %d", count, policy.HourlyReplyLimit)
	}
	if !last.IsZero() && now.Sub(last) < policy.ReplyCooldown {
		return false, fmt.Sprintf("距离上次回复不足 %s", policy.ReplyCooldown)
	}
	return true, ""
}

func (s *Scheduler) runTriage(ctx context.Context, p *Project, history []ChatMessage, msg Inbound) (*TriageDecision, error) {
	prompt := buildObservePrompt(p, history, msg)
	images, listing, cleanup, err := saveChatAttachments(msg)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	if len(listing) > 0 {
		prompt += "\n\n附件（已经下载到本机，可按路径读取）：\n" + strings.Join(listing, "\n")
	}
	timeout := s.cfg.TurnTimeout
	if timeout <= 0 || timeout > 5*time.Minute {
		timeout = 5 * time.Minute
	}
	spec := codexrunner.Spec{
		WorkDir:          chatWorkDir(p),
		Env:              s.resolveModelEnv(ctx, p),
		Prompt:           prompt,
		Images:           images,
		CodexPath:        s.cfg.CodexPath,
		Model:            firstNonEmpty(p.Model, s.cfg.Model),
		Effort:           firstNonEmpty(p.Effort, s.cfg.Effort, "low"),
		BaseInstructions: ObserveSystemPrompt,
		Sandbox:          firstNonEmpty(s.cfg.CodexSandbox, "read-only"),
		Timeout:          timeout,
		KeepResponse:     true,
	}
	res, err := s.runner.Run(ctx, spec)
	if err != nil {
		return nil, err
	}
	if res == nil || strings.TrimSpace(res.FinalResponse) == "" {
		return nil, fmt.Errorf("scheduler: triage worker returned no output")
	}
	return ParseTriageDecision(res.FinalResponse)
}

// handleObserved 处理非 @ 群消息：由观察模型逐条判断该回答、建单还是沉默；
// smart 模式只跳过没有信息的短消息，all 模式连这类也交给模型。
func (s *Scheduler) handleObserved(ctx context.Context, p *Project, msg Inbound, history []ChatMessage, force bool) error {
	policy := normalizeChatPolicy(p.Policy)
	if isNaturalControlCommand(msg.Text) {
		_ = s.repo.MarkMessage(ctx, msg.PlatformMessageID, "control_blocked", "副作用命令必须 @ 机器人或使用明确命令", 0, "")
		return nil
	}
	if !force && !shouldObserve(msg.Text) {
		status := "ignored"
		reason := "消息没有文本信息量（单字/表情/纯符号）"
		if policy.Shadow {
			status = "shadow_silent"
		}
		_ = s.repo.MarkMessage(ctx, msg.PlatformMessageID, status, reason, 0, "")
		_ = s.repo.AppendChat(ctx, ChatMessage{ChatID: msg.ChatID(), ProjectID: p.ID, Role: "observer", Body: "观察：silent（" + reason + "）"})
		return nil
	}
	decision, err := s.runTriage(ctx, p, history, msg)
	if err != nil {
		log.Printf("[scheduler] triage failed chat=%s msgId=%s: %v", msg.ChatID(), msg.PlatformMessageID, err)
		_ = s.repo.MarkMessage(ctx, msg.PlatformMessageID, "failed", clamp("观察判断失败: "+err.Error(), 1000), 0, "")
		return nil
	}
	summary := fmt.Sprintf("action=%s confidence=%.2f reason=%s", decision.Action, decision.Confidence, clamp(decision.Reason, 300))
	if policy.Shadow {
		status := "shadow_" + decision.Action
		detail := summary
		if decision.Action == "answer" {
			detail += "；候选回复：" + clamp(decision.Reply, 300)
		}
		if decision.Action == "triage" {
			detail += "；候选：" + firstNonEmpty(decision.Title, fmt.Sprintf("Issue #%d", decision.Issue))
		}
		_ = s.repo.MarkMessage(ctx, msg.PlatformMessageID, status, clamp(detail, 1000), 0, "")
		_ = s.repo.AppendChat(ctx, ChatMessage{ChatID: msg.ChatID(), ProjectID: p.ID, Role: "observer", Body: "影子判断：" + detail})
		return nil
	}

	switch decision.Action {
	case "silent":
		_ = s.repo.MarkMessage(ctx, msg.PlatformMessageID, "silent", clamp(summary, 1000), 0, "")
		_ = s.repo.AppendChat(ctx, ChatMessage{ChatID: msg.ChatID(), ProjectID: p.ID, Role: "observer", Body: "观察：silent（" + decision.Reason + "）"})
		return nil
	case "answer":
		if decision.Confidence < policy.AnswerConfidence {
			reason := fmt.Sprintf("置信度 %.2f 低于回答阈值 %.2f；%s", decision.Confidence, policy.AnswerConfidence, summary)
			_ = s.repo.MarkMessage(ctx, msg.PlatformMessageID, "triage_candidate", clamp(reason, 1000), 0, "")
			_ = s.repo.AppendChat(ctx, ChatMessage{ChatID: msg.ChatID(), ProjectID: p.ID, Role: "observer", Body: "观察：answer 候选，" + reason})
			return nil
		}
		if ok, reason := s.allowAutoReply(ctx, p, msg.ChatID()); !ok {
			_ = s.repo.MarkMessage(ctx, msg.PlatformMessageID, "rate_limited", reason, 0, "")
			return nil
		}
		if err := s.notify(ctx, msg.BotID, msg.ChatID(), msg.PlatformMessageID, decision.Reply); err != nil {
			return err
		}
		_ = s.repo.MarkMessage(ctx, msg.PlatformMessageID, "answered", clamp(decision.Reply, 500), 0, "")
		_ = s.repo.AppendChat(ctx, ChatMessage{ChatID: msg.ChatID(), ProjectID: p.ID, Role: "assistant", Body: decision.Reply})
		return nil
	case "triage":
		if decision.Confidence < policy.AutoIssueConfidence {
			reason := fmt.Sprintf("置信度 %.2f 低于自动建单阈值 %.2f；%s", decision.Confidence, policy.AutoIssueConfidence, summary)
			_ = s.repo.MarkMessage(ctx, msg.PlatformMessageID, "triage_candidate", clamp(reason, 1000), 0, "")
			_ = s.repo.AppendChat(ctx, ChatMessage{ChatID: msg.ChatID(), ProjectID: p.ID, Role: "observer", Body: "观察：triage 候选，" + reason})
			return nil
		}
		if !policy.AutoIssueEnabled {
			reason := "自动建单未开启；" + summary
			_ = s.repo.MarkMessage(ctx, msg.PlatformMessageID, "triage_candidate", clamp(reason, 1000), 0, "")
			_ = s.repo.AppendChat(ctx, ChatMessage{ChatID: msg.ChatID(), ProjectID: p.ID, Role: "observer", Body: "观察：triage 候选，" + reason})
			return nil
		}
		count, err := s.repo.CountAutoIssueDrafts(ctx, p.ID, msg.ChatID(), s.now().Add(-24*time.Hour))
		if err != nil {
			log.Printf("[scheduler] count auto issue drafts %s: %v", msg.ChatID(), err)
		}
		if count >= policy.DailyAutoIssueLimit {
			reason := fmt.Sprintf("本群 24 小时内自动建单草稿已达上限 %d；%s", policy.DailyAutoIssueLimit, summary)
			_ = s.repo.MarkMessage(ctx, msg.PlatformMessageID, "triage_candidate", clamp(reason, 1000), 0, "")
			_ = s.repo.AppendChat(ctx, ChatMessage{ChatID: msg.ChatID(), ProjectID: p.ID, Role: "observer", Body: "观察：triage 候选，" + reason})
			return nil
		}
		if ok, reason := s.allowAutoReply(ctx, p, msg.ChatID()); !ok {
			_ = s.repo.MarkMessage(ctx, msg.PlatformMessageID, "rate_limited", reason, 0, "")
			return nil
		}
		action := "issue"
		if decision.Issue > 0 {
			action = "issue_update"
		}
		reply := firstNonEmpty(decision.Reply, decision.Title)
		if reply == "" {
			reply = "群聊补充：" + msg.Text
		}
		chatDecision := &ChatDecision{
			Action: action, Reply: reply, Title: decision.Title,
			Body: decision.Body, Severity: decision.Severity, Issue: decision.Issue,
		}
		msg.Auto = true
		outcome, err := s.applyChatDecision(ctx, p, msg, chatDecision, decision.Issue)
		if err != nil {
			_ = s.repo.MarkMessage(ctx, msg.PlatformMessageID, "failed", clamp(err.Error(), 1000), outcome.Issue, outcome.IssueURL)
			return err
		}
		_ = s.repo.MarkMessage(ctx, msg.PlatformMessageID, outcome.Status, clamp(summary+"；"+outcome.Reason, 1000), outcome.Issue, outcome.IssueURL)
		_ = s.repo.AppendChat(ctx, ChatMessage{ChatID: msg.ChatID(), ProjectID: p.ID, Role: "assistant", Body: decisionSummary(chatDecision, outcome)})
		if outcome.Issue > 0 {
			_ = s.repo.PutLink(ctx, Link{PlatformMessageID: msg.PlatformMessageID, ProjectID: p.ID, Issue: outcome.Issue, ChatID: msg.ChatID()})
		}
		return nil
	}
	return nil
}
