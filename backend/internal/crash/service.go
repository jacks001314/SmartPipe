package crash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"smartpipe/backend/internal/github"
	"smartpipe/backend/internal/issuedraft"
	"smartpipe/backend/internal/store"
)

// Forge 是建单与评论所需的最小 GitHub 能力。
type Forge interface {
	CreateIssue(ctx context.Context, repo, title, body string, labels []string) (*github.Issue, error)
	Comment(ctx context.Context, repo string, number int, body string) error
}

// Service 处理崩溃上报：校验 → 限流 → 指纹去重 → 建单。
type Service struct {
	Store *store.Store
	// Forge 按项目解析 GitHub 客户端；nil 时由调用方保证不可用。
	Forge        func(ctx context.Context, projectID *string) (Forge, error)
	Now          func() time.Time
	MaxBytes     int
	RateLimit    int
	RateWindow   time.Duration
	DedupeWindow time.Duration
}

// Ingest 是一次上报请求。
type Ingest struct {
	Project *store.Project
	Repo    string
	Source  string
	Bundle  Bundle
	RawSize int

	// 群会话信息用于建单确认；自动建单必须先回到来源群等待明确确认。
	MessageID  string
	ChatID     string
	BotID      string
	BotName    string
	GroupName  string
	SenderID   string
	SenderName string
}

// Outcome 是上报结果。
type Outcome struct {
	Status      string             `json:"status"`
	Report      *store.CrashReport `json:"report,omitempty"`
	DraftID     string             `json:"draft_id,omitempty"`
	Prompt      string             `json:"prompt,omitempty"`
	IssueNumber int                `json:"issue_number"`
	IssueURL    string             `json:"issue_url"`
	Occurrences int                `json:"occurrences"`
	Similar     []Similar          `json:"similar,omitempty"`
	Reason      string             `json:"reason,omitempty"`
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) maxBytes() int {
	if s.MaxBytes > 0 {
		return s.MaxBytes
	}
	return 256 * 1024
}

func (s *Service) dedupeWindow() time.Duration {
	if s.DedupeWindow > 0 {
		return s.DedupeWindow
	}
	return 24 * time.Hour
}

func (s *Service) rateLimit() (int, time.Duration) {
	limit := s.RateLimit
	if limit <= 0 {
		limit = 5
	}
	window := s.RateWindow
	if window <= 0 {
		window = time.Hour
	}
	return limit, window
}

// Ingest 处理一条崩溃上报。
func (s *Service) Ingest(ctx context.Context, in Ingest) (*Outcome, error) {
	if s.Store == nil {
		return nil, errors.New("crash: store is required")
	}
	now := s.now().UTC()
	if strings.TrimSpace(in.Source) == "" {
		in.Source = "lanxin"
	}
	var projectID *string
	if in.Project != nil {
		id := in.Project.ID
		projectID = &id
	}

	var rejectReason string
	isRateLimited := false
	if in.RawSize > s.maxBytes() {
		rejectReason = fmt.Sprintf("bundle too large: %d > %d bytes", in.RawSize, s.maxBytes())
	} else if in.Bundle.UserID != "" {
		limit, window := s.rateLimit()
		count, err := s.Store.CountCrashesByUser(ctx, projectID, in.Bundle.UserID, now.Add(-window))
		if err != nil {
			return nil, err
		}
		if count >= limit {
			rejectReason = fmt.Sprintf("rate limited: %d reports in %s", count, window)
			isRateLimited = true
		}
	}

	var normalized *Normalized
	if rejectReason == "" {
		n, err := in.Bundle.Normalize(now)
		if err != nil {
			rejectReason = err.Error()
		} else {
			normalized = n
		}
	}
	if rejectReason != "" {
		report, err := s.Store.CreateCrashReport(ctx, s.rejectedRow(in, projectID, now, rejectReason))
		if err != nil {
			return nil, err
		}
		status := "rejected"
		if isRateLimited {
			status = "rate_limited"
		}
		return &Outcome{Status: status, Report: report, Reason: rejectReason}, nil
	}

	b := normalized.Bundle
	if strings.TrimSpace(in.ChatID) == "" {
		return nil, errors.New("crash: chat_id is required so the issue can be confirmed in its source group")
	}
	if previous, err := s.Store.FindCrashByFingerprint(ctx, projectID, normalized.Fingerprint, now.Add(-s.dedupeWindow())); err == nil {
		occurrences, bumpErr := s.Store.BumpCrashReport(ctx, previous.ID, now)
		if bumpErr != nil {
			return nil, bumpErr
		}
		if s.Forge != nil && previous.IssueNumber > 0 {
			if client, cerr := s.Forge(ctx, projectID); cerr == nil {
				_ = client.Comment(ctx, previous.Repo, previous.IssueNumber, DupComment(*normalized, occurrences, previous.ID, now))
			}
		}
		return &Outcome{Status: "deduped", Report: previous, IssueNumber: previous.IssueNumber,
			IssueURL: previous.IssueURL, Occurrences: occurrences}, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	labels := []string{"crash", "crash_report", "source:" + in.Source}
	raw, _ := json.Marshal(b)
	occurred := normalized.OccurredAt
	report, err := s.Store.CreateCrashReport(ctx, store.CrashReport{
		ProjectID: projectID, Fingerprint: normalized.Fingerprint, App: b.App, Version: b.Version,
		OS: b.OS, Arch: b.Arch, UserID: b.UserID, DeviceID: b.DeviceID, Source: in.Source,
		Repo: in.Repo, Status: "awaiting_confirmation", Occurrences: 1, SizeBytes: normalized.SizeBytes,
		Bundle: raw, OccurredAt: &occurred,
	})
	if err != nil {
		return nil, err
	}
	similar := s.findSimilar(ctx, projectID, b, normalized.Fingerprint)
	if rawSimilar, merr := json.Marshal(similar); merr == nil {
		_ = s.Store.SetCrashSimilar(ctx, report.ID, rawSimilar)
		report.Similar = rawSimilar
	}
	body := Body(*normalized, in.Source, report.ID, 1, now) + SimilarSection(similar)
	draftID, err := issuedraft.NewID()
	if err != nil {
		_ = s.Store.UpdateCrashIssue(ctx, report.ID, 0, "", "rejected", "draft id failed: "+err.Error())
		return nil, fmt.Errorf("crash: draft id: %w", err)
	}
	sourceID := strings.TrimSpace(in.MessageID)
	if sourceID == "" {
		sourceID = "crash-report-" + strconv.FormatInt(report.ID, 10)
	}
	draft := &issuedraft.Draft{
		ID: draftID, Kind: issuedraft.KindCrash, ProjectID: valueOr(projectID, ""), ChatID: in.ChatID,
		SourceMessageID: sourceID, BotID: in.BotID, BotName: in.BotName,
		GroupID: in.ChatID, GroupName: in.GroupName, SenderID: in.SenderID, SenderName: in.SenderName,
		SourceText: "崩溃上报：" + Title(b), Title: Title(b), Body: body, Severity: "high",
		RefID: strconv.FormatInt(report.ID, 10), Repo: in.Repo, Labels: labels,
		Status: issuedraft.StatusPending, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(issuedraft.TTL),
	}
	created, err := s.Store.CreateIssueDraft(ctx, draft)
	if err != nil {
		_ = s.Store.UpdateCrashIssue(ctx, report.ID, 0, "", "rejected", "issue draft failed: "+err.Error())
		return nil, fmt.Errorf("crash: create issue draft: %w", err)
	}
	if !created {
		existing, getErr := s.Store.IssueDraftBySource(ctx, sourceID)
		if getErr != nil {
			return nil, getErr
		}
		if existing == nil {
			_ = s.Store.UpdateCrashIssue(ctx, report.ID, 0, "", "rejected", "duplicate draft source has no draft")
			return nil, errors.New("crash: duplicate draft source has no draft")
		}
		if existing.Status == issuedraft.StatusCreated {
			report.Status = "created"
			report.IssueNumber = existing.Issue
			report.IssueURL = existing.IssueURL
			return &Outcome{Status: "deduped", Report: report, DraftID: existing.ID,
				IssueNumber: existing.Issue, IssueURL: existing.IssueURL, Occurrences: 1, Similar: similar}, nil
		}
		if existing.Status != issuedraft.StatusPending && existing.Status != issuedraft.StatusCreating {
			return &Outcome{Status: "deduped", Report: report, DraftID: existing.ID, Occurrences: 1, Similar: similar}, nil
		}
		draft = existing
	}
	report.Status = "awaiting_confirmation"
	return &Outcome{Status: "awaiting_confirmation", Report: report, DraftID: draft.ID,
		Prompt: issuedraft.Prompt(draft), Occurrences: 1, Similar: similar}, nil
}

func valueOr(v *string, fallback string) string {
	if v != nil {
		return *v
	}
	return fallback
}

func (s *Service) rejectedRow(in Ingest, projectID *string, now time.Time, reason string) store.CrashReport {
	b := Scrub(in.Bundle)
	raw, _ := json.Marshal(b)
	return store.CrashReport{
		ProjectID: projectID, Fingerprint: b.Fingerprint, App: b.App, Version: b.Version,
		OS: b.OS, Arch: b.Arch, UserID: b.UserID, DeviceID: b.DeviceID, Source: in.Source,
		Repo: in.Repo, Status: "rejected", RejectReason: reason, Occurrences: 0,
		SizeBytes: in.RawSize, Bundle: raw, OccurredAt: &now,
	}
}

// findSimilar 在最近 90 天内同应用的已建单崩溃里找栈相似的历史问题。
func (s *Service) findSimilar(ctx context.Context, projectID *string, b Bundle, fingerprint string) []Similar {
	if strings.TrimSpace(b.Exception.Stack) == "" {
		return nil
	}
	rows, err := s.Store.ListCrashesByApp(ctx, projectID, b.App, s.now().UTC().AddDate(0, 0, -90), 100)
	if err != nil {
		return nil
	}
	items := make([]Similar, 0, len(rows))
	for _, r := range rows {
		if r.Fingerprint == fingerprint || r.IssueNumber <= 0 {
			continue
		}
		var prev Bundle
		if err := json.Unmarshal(r.Bundle, &prev); err != nil {
			continue
		}
		score := StackSimilarity(b, prev)
		if score <= 0 {
			continue
		}
		items = append(items, Similar{
			ReportID: r.ID, IssueNumber: r.IssueNumber, IssueURL: r.IssueURL,
			Fingerprint: r.Fingerprint, Version: r.Version, Score: score,
		})
	}
	return topSimilar(items, 0.5, 3)
}
