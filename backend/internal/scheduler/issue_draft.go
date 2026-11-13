package scheduler

import (
	"context"
	"fmt"
	"log"
	"strings"

	"smartpipe/backend/internal/issuedraft"
)

const issueDraftTTL = issuedraft.TTL

const (
	IssueDraftPending    = issuedraft.StatusPending
	IssueDraftCreating   = issuedraft.StatusCreating
	IssueDraftCreated    = issuedraft.StatusCreated
	IssueDraftCancelled  = issuedraft.StatusCancelled
	IssueDraftSuperseded = issuedraft.StatusSuperseded
	IssueDraftExpired    = issuedraft.StatusExpired
)

var (
	ErrIssueDraftNotFound = issuedraft.ErrNotFound
	issueDraftIDPattern   = issuedraft.IDPattern
)

// IssueDraft 是建单草稿：落库后立即走建单路径；建单失败时保留草稿，群里可以重试。
type IssueDraft = issuedraft.Draft

// draftSourceInbound 还原建单需要的来源信息；附件不会进入 Issue 正文。
func draftSourceInbound(d *IssueDraft) Inbound {
	if d == nil {
		return Inbound{}
	}
	chatType := "p2p"
	if strings.TrimSpace(d.GroupID) != "" {
		chatType = "group"
	}
	return Inbound{
		Platform: "lanxin", BotID: d.BotID, BotName: d.BotName,
		PlatformMessageID: d.SourceMessageID, ChatType: chatType,
		SenderID: d.SenderID, SenderName: d.SenderName,
		GroupID: d.GroupID, GroupName: d.GroupName, Text: d.SourceText, IsAtMe: true,
	}
}

// draftDecision 还原 chat worker 的结构化建单内容。
func draftDecision(d *IssueDraft) *ChatDecision {
	if d == nil {
		return nil
	}
	return &ChatDecision{Action: "issue", Title: d.Title, Body: d.Body, Severity: d.Severity}
}

func (s *Scheduler) newIssueDraft(ctx context.Context, p *Project, msg Inbound, d *ChatDecision) (*IssueDraft, error) {
	id, err := issuedraft.NewID()
	if err != nil {
		return nil, fmt.Errorf("scheduler: generate issue draft id: %w", err)
	}
	now := s.now()
	draft := &IssueDraft{
		ID: id, Kind: issuedraft.KindChat,
		ProjectID: p.ID, ChatID: msg.ChatID(), SourceMessageID: msg.PlatformMessageID,
		BotID: msg.BotID, BotName: msg.BotName, GroupID: msg.GroupID, GroupName: msg.GroupName,
		SenderID: msg.SenderID, SenderName: msg.SenderName, SourceText: msg.Text,
		Title: d.Title, Body: d.Body, Severity: d.Severity, Auto: msg.Auto,
		Status: IssueDraftPending, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(issueDraftTTL),
	}
	created, err := s.repo.CreateIssueDraft(ctx, draft)
	if err != nil {
		return nil, err
	}
	if !created {
		current, getErr := s.repo.LatestPendingIssueDraft(ctx, p.ID, msg.ChatID(), now)
		if getErr != nil {
			return nil, getErr
		}
		if current != nil {
			return current, nil
		}
		return nil, ErrIssueDraftNotFound
	}
	return draft, nil
}

func issueDraftPrompt(d *IssueDraft) string {
	return issuedraft.Prompt(d)
}

func parseIssueDraftCommand(text string) (action, draftID string, ok bool) {
	return issuedraft.ParseCommand(text)
}

func (s *Scheduler) replyIssueDraft(ctx context.Context, p *Project, msg Inbound, status, text string, issue int, issueURL string) error {
	if err := s.notify(ctx, msg.BotID, msg.ChatID(), msg.PlatformMessageID, text); err != nil {
		_ = s.repo.MarkMessage(ctx, msg.PlatformMessageID, "failed", err.Error(), issue, issueURL)
		return err
	}
	_ = s.repo.AppendChat(ctx, ChatMessage{ChatID: msg.ChatID(), ProjectID: p.ID, Role: "assistant", Body: text})
	_ = s.repo.MarkMessage(ctx, msg.PlatformMessageID, status, clamp(text, 500), issue, issueURL)
	if issue > 0 {
		_ = s.repo.PutLink(ctx, Link{PlatformMessageID: msg.PlatformMessageID, ProjectID: p.ID, Issue: issue, ChatID: msg.ChatID()})
	}
	return nil
}

// answerIssueDraft 处理群里的“确认建单/取消建单”命令。
// 建单默认自动完成，这条命令用于重试失败的草稿，以及取消还在等待中的草稿。
func (s *Scheduler) answerIssueDraft(ctx context.Context, p *Project, msg Inbound) (bool, error) {
	action, explicitID, ok := parseIssueDraftCommand(msg.Text)
	if !ok {
		return false, nil
	}
	now := s.now()
	draft, err := s.repo.LatestPendingIssueDraft(ctx, p.ID, msg.ChatID(), now)
	if err != nil {
		return true, s.replyIssueDraft(ctx, p, msg, "failed", "暂时查不到待确认草稿，请稍后再试。", 0, "")
	}
	if draft == nil {
		return true, s.replyIssueDraft(ctx, p, msg, "need_info", "当前没有待确认的建单草稿。请先发送问题描述，我会生成草稿供群里确认。", 0, "")
	}
	if explicitID != "" && !strings.EqualFold(explicitID, draft.ID) {
		return true, s.replyIssueDraft(ctx, p, msg, "need_info",
			fmt.Sprintf("这个草稿编号不存在或已失效。当前待确认的是 %s。", draft.ID), 0, "")
	}
	draft, err = s.repo.ClaimIssueDraft(ctx, draft.ID, p.ID, msg.ChatID(), msg.SenderID, msg.SenderName, now)
	if err != nil {
		return true, s.replyIssueDraft(ctx, p, msg, "failed", "草稿状态更新失败，请稍后重试。", 0, "")
	}
	if draft == nil {
		return true, s.replyIssueDraft(ctx, p, msg, "need_info", "这个草稿已被处理或已经过期，请重新发送问题描述。", 0, "")
	}
	if action == "cancel" {
		_ = s.repo.FinishIssueDraft(ctx, draft.ID, IssueDraftCancelled, 0, "", now)
		_ = s.repo.MarkIssueDraftSource(ctx, draft, IssueDraftCancelled, 0, "")
		text := fmt.Sprintf("已取消建单草稿 %s，不会创建 Issue。", draft.ID)
		return true, s.replyIssueDraft(ctx, p, msg, "issue_draft_cancelled", text, 0, "")
	}
	_, _, confirmErr := s.confirmIssueDraft(ctx, p, msg, draft)
	return true, confirmErr
}

// confirmIssueDraft 创建 Issue 并把 worker 入队，返回 Issue 编号与地址。
// 自动建单和群内重试共用这一条路径，保证两种入口的行为一致。
func (s *Scheduler) confirmIssueDraft(ctx context.Context, p *Project, msg Inbound, draft *IssueDraft) (int, string, error) {
	source := draftSourceInbound(draft)
	existing, err := s.findDuplicate(ctx, p.ID, draft.Title)
	if err != nil {
		log.Printf("[scheduler] issue draft dedupe failed: %v", err)
	}
	duplicate := existing != nil

	if !duplicate {
		body := buildIssueBody(source, draftDecision(draft))
		if draft.Kind == issuedraft.KindCrash {
			body = draft.Body
		}
		labels := draft.Labels
		if len(labels) == 0 {
			labels = []string{"from-lanxin", "severity/" + draft.Severity}
		}
		createdIssue, createErr := s.tracker.CreateIssue(ctx, p.ID, draft.Title, body, labels)
		err = createErr
		existing = &createdIssue
		if err != nil {
			_ = s.repo.ReleaseIssueDraft(ctx, draft.ID, s.now())
			_ = s.replyIssueDraft(ctx, p, msg, "failed",
				fmt.Sprintf("GitHub 建单失败，草稿 %s 已保留，稍后回复「确认建单 %s」重试。", draft.ID, draft.ID), 0, "")
			return 0, "", err
		}
	} else {
		if err := s.tracker.Comment(ctx, p.ID, existing.Number,
			botComment("来自蓝信的新反馈（重复问题）：\n\n"+draft.SourceText)); err != nil {
			_ = s.repo.ReleaseIssueDraft(ctx, draft.ID, s.now())
			_ = s.replyIssueDraft(ctx, p, msg, "failed",
				fmt.Sprintf("补充已有 Issue 失败，草稿 %s 已保留，稍后回复「确认建单 %s」重试。", draft.ID, draft.ID), 0, "")
			return 0, "", err
		}
	}

	if err := s.repo.FinishIssueDraft(ctx, draft.ID, IssueDraftCreated, existing.Number, existing.URL, s.now()); err != nil {
		log.Printf("[scheduler] finish issue draft %s: %v", draft.ID, err)
	}
	if err := s.repo.MarkIssueDraftSource(ctx, draft, IssueDraftCreated, existing.Number, existing.URL); err != nil {
		log.Printf("[scheduler] mark issue draft source %s: %v", draft.ID, err)
	}
	if w, _ := s.repo.ActiveWorker(ctx, p.ID, existing.Number); w != nil {
		s.resume(w, source.Text)
	} else {
		created, qerr := s.queueWorker(ctx, &Worker{
			ProjectID: p.ID, Issue: existing.Number, IssueTitle: existing.Title,
			IssueURL: existing.URL, ChatID: msg.ChatID(), State: StateQueued,
		}, "建单后 worker 已入队")
		if qerr != nil {
			_ = s.replyIssueDraft(ctx, p, msg, "issue_created",
				fmt.Sprintf("Issue #%d 已创建，但 worker 入队失败：%s\n%s", existing.Number, shortErr(qerr), existing.URL),
				existing.Number, existing.URL)
			return existing.Number, existing.URL, qerr
		}
		if created {
			s.Wake()
		}
	}

	text := fmt.Sprintf("已建 Issue #%d：%s\n%s", existing.Number, clamp(existing.Title, 40), existing.URL)
	status := "issue_created"
	if duplicate {
		text = fmt.Sprintf("已并入 Issue #%d 的讨论：%s", existing.Number, existing.URL)
		status = "issue_updated"
	}
	if replyErr := s.replyIssueDraft(ctx, p, msg, status, text, existing.Number, existing.URL); replyErr != nil {
		return existing.Number, existing.URL, replyErr
	}
	return existing.Number, existing.URL, nil
}
