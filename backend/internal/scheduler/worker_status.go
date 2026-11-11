package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// isWorkerStatusQuestion 识别“有没有派发 worker / 修复到哪了 / PR 状态”这类
// 可以直接从调度库回答的问题。普通的需求描述或“去修复”请求不在这里拦截。
func isWorkerStatusQuestion(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" {
		return false
	}
	if strings.Contains(t, "/status") {
		return true
	}
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(t, w) {
				return true
			}
		}
		return false
	}
	status := has("状态", "进度", "有没有", "是否", "到哪", "开始修", "在修",
		"修完", "修复了吗", "有修复吗", "修好了吗", "完成了吗")
	return (has("worker") && (status || has("派发"))) ||
		(has("派发") && (status || has("修复") || has("worker"))) ||
		(has("修复") && has("进度", "到哪", "开始修", "在修", "修完", "修复了吗", "有修复吗", "修好了吗", "完成了吗")) ||
		(has("pr") && status) ||
		(has("进度") && (has("#") || has("issue")))
}

// parseWorkerControl 解析群内控制命令和少量自然语言说法。
// 返回 explicit=true 表示用户明确写了 /命令，缺少 Issue 编号时应直接提示用法。
func parseWorkerControl(text string) (action string, explicit bool) {
	fields := strings.Fields(strings.ToLower(text))
	for _, f := range fields {
		if !strings.HasPrefix(f, "/") {
			continue
		}
		switch strings.TrimPrefix(f, "/") {
		case "status":
			return "status", true
		case "pause", "resume", "retry", "stop", "fix":
			return strings.TrimPrefix(f, "/"), true
		}
	}
	t := strings.ToLower(text)
	switch {
	case strings.Contains(t, "暂停") || strings.Contains(t, "停一下"):
		return "pause", false
	case strings.Contains(t, "恢复") || strings.Contains(t, "继续") || strings.Contains(t, "接着跑"):
		return "resume", false
	case strings.Contains(t, "重试") || strings.Contains(t, "重新跑") || strings.Contains(t, "再跑一次"):
		return "retry", false
	case strings.Contains(t, "停止") || strings.Contains(t, "停掉") || strings.Contains(t, "终止"):
		return "stop", false
	case strings.Contains(t, "派发") && strings.Contains(t, "worker") &&
		!strings.Contains(t, "吗") && !strings.Contains(t, "有没有") && !strings.Contains(t, "是否"):
		return "fix", false
	case strings.Contains(t, "重新派发") || strings.Contains(t, "重新排队"):
		return "fix", false
	default:
		return "", false
	}
}

// recentIssueFromHistory 在用户没有写 #编号 时，从最近几轮会话里找回正在讨论的 Issue。
// 优先采用助手的“已建单/已补充到 Issue #N”摘要，避免把普通代码片段里的 # 当成 Issue。
func recentIssueFromHistory(history []ChatMessage) int {
	start := len(history) - 8
	if start < 0 {
		start = 0
	}
	for i := len(history) - 1; i >= start; i-- {
		if history[i].Role != "assistant" {
			continue
		}
		if n := referencedIssue(history[i].Body); n > 0 {
			return n
		}
	}
	for i := len(history) - 1; i >= start; i-- {
		if n := referencedIssue(history[i].Body); n > 0 {
			return n
		}
	}
	return 0
}

// workerForIssue 优先返回进行中的 worker；没有进行中的任务时返回最近一次
// 已结束或失败的 worker，便于群里查看失败原因并执行 /retry 或 /fix。
func (s *Scheduler) workerForIssue(ctx context.Context, projectID string, issue int) (*Worker, error) {
	if w, err := s.repo.ActiveWorker(ctx, projectID, issue); err != nil || w != nil {
		return w, err
	}
	workers, err := s.repo.ListWorkers(ctx)
	if err != nil {
		return nil, err
	}
	var found *Worker
	for _, w := range workers {
		if w == nil || w.ProjectID != projectID || w.Issue != issue {
			continue
		}
		if found == nil || w.UpdatedAt.After(found.UpdatedAt) {
			found = w
		}
	}
	return found, nil
}

func workerStateLabel(s State) string {
	switch s {
	case StateQueued:
		return "已入队，等待执行名额"
	case StatePreparing:
		return "准备中"
	case StateCoding:
		return "正在修改代码"
	case StateVerifying:
		return "正在验证"
	case StatePushing:
		return "正在提交、推送或创建 PR"
	case StateAwaitingInput:
		return "等待补充信息"
	case StateAwaitingReview:
		return "等待 review"
	case StatePaused:
		return "已暂停"
	case StateBlocked:
		return "已阻塞"
	case StateClosed:
		return "已关闭"
	case StateFailed:
		return "执行失败"
	default:
		return string(s)
	}
}

// workerStatusReply 用数据库里的真实 worker 状态回答群里的进度问题。
func workerStatusReply(issue int, w *Worker) string {
	if w == nil {
		return fmt.Sprintf("目前没有。Issue #%d 没有进行中的 worker。需要修复的话，请在群里补充 #%d 和修复要求，或在 Issue 评论里发送 `/fix`。", issue, issue)
	}
	if w.State == StateClosed {
		return fmt.Sprintf("Issue #%d 的 worker 已结束。如需重新修复，发送 `/fix #%d`。", issue, issue)
	}
	if w.State == StateFailed {
		return fmt.Sprintf("Issue #%d 的 worker 执行失败：%s。发送 `/retry #%d` 重试，或 `/fix #%d` 重新排队。",
			issue, clamp(firstNonEmpty(w.LastError, "未知错误"), 200), issue, issue)
	}
	parts := []string{fmt.Sprintf("有。Issue #%d 的 worker 当前状态：%s", issue, workerStateLabel(w.State))}
	if w.Branch != "" {
		parts = append(parts, "分支 `"+w.Branch+"`")
	}
	if w.PRNumber > 0 {
		parts = append(parts, fmt.Sprintf("PR #%d", w.PRNumber))
	}
	if w.PRURL != "" {
		parts = append(parts, w.PRURL)
	}
	if w.State == StateBlocked && w.BlockedReason != "" {
		parts = append(parts, "阻塞原因："+clamp(w.BlockedReason, 200))
	}
	if w.State == StateFailed && w.LastError != "" {
		parts = append(parts, "错误："+clamp(w.LastError, 200))
	}
	return strings.Join(parts, "；")
}

func workerActionText(issue int, action string, w *Worker) string {
	switch action {
	case "pause":
		return fmt.Sprintf("已暂停 Issue #%d 的 worker。恢复请发送 `/resume #%d`。", issue, issue)
	case "resume":
		return fmt.Sprintf("已恢复 Issue #%d 的 worker，已重新排队。", issue)
	case "retry":
		return fmt.Sprintf("已重试 Issue #%d 的 worker，已重新排队。", issue)
	case "stop":
		return fmt.Sprintf("已停止 Issue #%d 的 worker。", issue)
	case "fix":
		return fmt.Sprintf("已为 Issue #%d 重新排队 worker。", issue)
	default:
		return workerStatusReply(issue, w)
	}
}

// replyWorker 统一记录并发送 worker 相关回复。
func (s *Scheduler) replyWorker(ctx context.Context, p *Project, msg Inbound, issue int, text string) error {
	if err := s.notify(ctx, msg.BotID, msg.ChatID(), msg.PlatformMessageID, text); err != nil {
		_ = s.repo.MarkMessage(ctx, msg.PlatformMessageID, "failed", err.Error(), issue, issueURLFor(p, issue))
		return err
	}
	_ = s.repo.AppendChat(ctx, ChatMessage{ChatID: msg.ChatID(), ProjectID: p.ID, Role: "assistant", Body: text})
	_ = s.repo.MarkMessage(ctx, msg.PlatformMessageID, "answered", text, issue, issueURLFor(p, issue))
	if issue > 0 {
		_ = s.repo.PutLink(ctx, Link{
			PlatformMessageID: msg.PlatformMessageID, ProjectID: p.ID, Issue: issue, ChatID: msg.ChatID(),
		})
	}
	return nil
}

// answerWorkerStatus 对明确的进度问题直接查询 worker，避免聊天模型不知道调度状态
// 而回答“没有派发 worker”。返回 handled=true 表示已经回复，调用方不再走模型。
func (s *Scheduler) answerWorkerStatus(ctx context.Context, p *Project, msg Inbound, history []ChatMessage) (bool, error) {
	if !isWorkerStatusQuestion(msg.Text) {
		return false, nil
	}
	issue := referencedIssue(msg.Text)
	if issue <= 0 {
		issue = recentIssueFromHistory(history)
	}
	if issue <= 0 {
		return true, s.replyWorker(ctx, p, msg, 0, "请在消息里带上 Issue 编号（例如 #210），我才能查看 worker 状态。")
	}
	w, err := s.workerForIssue(ctx, p.ID, issue)
	if err != nil {
		return true, s.replyWorker(ctx, p, msg, issue, "暂时查不到 worker 状态，请稍后再试。")
	}
	return true, s.replyWorker(ctx, p, msg, issue, workerStatusReply(issue, w))
}

// answerWorkerControl 处理群内的 worker 控制动作：暂停、恢复、重试、停止、重新排队。
func (s *Scheduler) answerWorkerControl(ctx context.Context, p *Project, msg Inbound, history []ChatMessage) (bool, error) {
	action, explicit := parseWorkerControl(msg.Text)
	if action == "" || action == "status" {
		return false, nil
	}
	if !msg.IsAtMe && !msg.ReplyToBot && !explicit {
		_ = s.repo.MarkMessage(ctx, msg.PlatformMessageID, "control_blocked", "副作用命令必须 @ 机器人或使用明确命令", 0, "")
		return false, nil
	}
	issue := referencedIssue(msg.Text)
	if issue <= 0 {
		issue = recentIssueFromHistory(history)
	}
	if issue <= 0 {
		if explicit {
			return true, s.replyWorker(ctx, p, msg, 0, fmt.Sprintf("请在命令里带上 Issue 编号，例如 `/%s #210`。", action))
		}
		return false, nil
	}
	w, err := s.workerForIssue(ctx, p.ID, issue)
	if err != nil {
		return true, s.replyWorker(ctx, p, msg, issue, "暂时查不到 worker 状态，请稍后再试。")
	}

	var result *Worker
	switch action {
	case "pause":
		if w == nil {
			return true, s.replyWorker(ctx, p, msg, issue, fmt.Sprintf("Issue #%d 当前没有进行中的 worker，无法暂停。", issue))
		}
		result, err = s.PauseWorker(ctx, w.ID)
	case "resume":
		if w == nil {
			return true, s.replyWorker(ctx, p, msg, issue, fmt.Sprintf("Issue #%d 当前没有可恢复的 worker；发送 `/fix #%d` 可以重新排队。", issue, issue))
		}
		result, err = s.ResumeWorker(ctx, w.ID)
	case "retry":
		if w == nil {
			return true, s.replyWorker(ctx, p, msg, issue, fmt.Sprintf("Issue #%d 当前没有可重试的 worker；发送 `/fix #%d` 可以重新排队。", issue, issue))
		}
		result, err = s.RetryWorker(ctx, w.ID)
	case "stop":
		if w == nil {
			return true, s.replyWorker(ctx, p, msg, issue, fmt.Sprintf("Issue #%d 当前没有进行中的 worker。", issue))
		}
		result, err = s.StopWorker(ctx, w.ID)
	case "fix":
		if w == nil || w.State == StateClosed || w.State == StateFailed {
			nw := &Worker{
				ProjectID: p.ID, Issue: issue, IssueURL: issueURLFor(p, issue),
				ChatID: msg.ChatID(), State: StateQueued,
			}
			created, qerr := s.queueWorker(ctx, nw, "来自蓝信命令 /fix，worker 已入队")
			if qerr != nil {
				return true, s.replyWorker(ctx, p, msg, issue, "重新排队失败，请稍后再试。")
			}
			if created {
				s.Wake()
			}
			result = nw
		} else {
			switch w.State {
			case StatePaused, StateBlocked, StateAwaitingInput, StateAwaitingReview:
				result, err = s.ResumeWorker(ctx, w.ID)
			default:
				return true, s.replyWorker(ctx, p, msg, issue,
					fmt.Sprintf("Issue #%d 已有 worker，当前状态：%s，无需重复派发。", issue, workerStateLabel(w.State)))
			}
		}
	}
	if err != nil {
		if errors.Is(err, ErrActionNotAllowed) {
			fresh, _ := s.workerForIssue(ctx, p.ID, issue)
			state := "未知"
			if fresh != nil {
				state = workerStateLabel(fresh.State)
			}
			return true, s.replyWorker(ctx, p, msg, issue,
				fmt.Sprintf("Issue #%d 当前状态：%s，无法执行该操作。", issue, state))
		}
		return true, s.replyWorker(ctx, p, msg, issue, "操作失败："+shortErr(err))
	}
	return true, s.replyWorker(ctx, p, msg, issue, workerActionText(issue, action, result))
}
