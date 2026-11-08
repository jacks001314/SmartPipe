// Package bridge 实现蓝信消息 ↔ GitHub Issue 的桥接：
// 入站消息去重、建 Issue、写映射表；Webhook 验签、去重并记录事件。
package bridge

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"smartpipe/backend/internal/github"
	"smartpipe/backend/internal/store"
)

// Message 是桥接服务接收的归一化入站消息。
type Message struct {
	Channel    string `json:"channel"`
	BotID      string `json:"bot_id"`
	BotName    string `json:"bot_name"`
	ChatType   string `json:"chat_type"`
	ChatID     string `json:"chat_id"`
	UserID     string `json:"user_id"`
	SenderName string `json:"sender_name"`
	GroupName  string `json:"group_name"`
	MessageID  string `json:"message_id"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	ProjectID  string `json:"project_id"`
}

// ErrInvalidMessage 表示消息缺少必要字段。
var ErrInvalidMessage = errors.New("bridge: invalid message")

// Service 编排去重与建单。
type Service struct {
	store      *store.Store
	clientFor  func(ctx context.Context, projectID *string) (*github.Client, error)
	dispatcher Dispatcher
	notifier   Notifier
}

// NewService 构造桥接服务；clientFor 为 nil 时使用凭据库解析。
func NewService(st *store.Store, clientFor func(context.Context, *string) (*github.Client, error)) *Service {
	if clientFor == nil {
		clientFor = func(ctx context.Context, projectID *string) (*github.Client, error) {
			return ClientFromCredentials(ctx, st, projectID)
		}
	}
	return &Service{store: st, clientFor: clientFor}
}

// WithDispatcher 挂上任务派发器；nil 表示只建单不派发。
func (s *Service) WithDispatcher(d Dispatcher) *Service {
	s.dispatcher = d
	return s
}

// WithNotifier 挂上来源会话回执（蓝信）；nil 表示不回执。
func (s *Service) WithNotifier(n Notifier) *Service {
	s.notifier = n
	return s
}

// HandleMessage 处理一条入站消息，返回映射与是否新建。
func (s *Service) HandleMessage(ctx context.Context, msg Message) (*store.BridgeLink, bool, error) {
	msg.MessageID = strings.TrimSpace(msg.MessageID)
	msg.Title = strings.TrimSpace(msg.Title)
	if msg.MessageID == "" {
		return nil, false, fmt.Errorf("%w: message_id is required", ErrInvalidMessage)
	}
	if msg.Title == "" {
		return nil, false, fmt.Errorf("%w: title is required", ErrInvalidMessage)
	}
	if msg.Channel == "" {
		msg.Channel = "lanxin"
	}

	if existing, err := s.store.GetBridgeLinkByMessage(ctx, msg.MessageID); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, false, err
	}

	var projectID *string
	if strings.TrimSpace(msg.ProjectID) != "" {
		v := msg.ProjectID
		projectID = &v
	}
	project, repo, labels, err := s.resolveTarget(ctx, projectID)
	if err != nil {
		return nil, false, err
	}

	client, err := s.clientFor(ctx, projectID)
	if err != nil {
		return nil, false, err
	}
	body := msg.Body
	if ident, ierr := s.store.FindUserIdentity(ctx, msg.Channel, msg.UserID, projectID); ierr == nil && ident != nil {
		name := strings.TrimSpace(ident.DisplayName)
		if name == "" {
			name = ident.GitHubLogin
		}
		body = fmt.Sprintf("> 上报人：%s（%s %s，GitHub @%s）\n\n%s", name, msg.Channel, msg.UserID, ident.GitHubLogin, body)
	}
	issue, err := client.CreateIssue(ctx, repo, msg.Title, body, labels)
	if err != nil {
		return nil, false, err
	}

	link, err := s.store.CreateBridgeLink(ctx, store.BridgeLink{
		SourceMessageID: msg.MessageID,
		Channel:         msg.Channel,
		ChatID:          msg.ChatID,
		UserID:          msg.UserID,
		ProjectID:       projectID,
		Repo:            repo,
		IssueNumber:     issue.Number,
		IssueURL:        issue.HTMLURL,
	})
	if errors.Is(err, store.ErrDuplicate) {
		existing, getErr := s.store.GetBridgeLinkByMessage(ctx, msg.MessageID)
		if getErr != nil {
			return nil, false, getErr
		}
		return existing, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	link = s.dispatch(ctx, link, project, msg.Title)
	s.receipt(ctx, link, msg)
	return link, true, nil
}

// receipt 把 issue_url 回给来源群；失败只记录，不影响建单。
func (s *Service) receipt(ctx context.Context, link *store.BridgeLink, msg Message) {
	if s.notifier == nil {
		_ = s.store.MarkReceipt(ctx, link.ID, "skipped", "no notifier configured")
		return
	}
	chatType := "group"
	if strings.TrimSpace(msg.ChatID) == "" {
		chatType = "p2p"
	}
	target := msg.ChatID
	if target == "" {
		target = msg.UserID
	}
	err := s.notifier.Reply(ctx, msg.BotID, chatType, target, msg.MessageID, BuildReceipt(link.IssueNumber, link.IssueURL))
	status, detail := "sent", ""
	if err != nil {
		status, detail = "failed", err.Error()
	}
	_ = s.store.MarkReceipt(ctx, link.ID, status, detail)
	_ = s.store.AppendAudit(ctx, store.AuditEntry{
		Actor: "bridge", Action: "bridge.receipt", TargetType: "issue_link",
		TargetID: formatID(link.ID), ProjectID: link.ProjectID, Result: status,
		Detail: map[string]any{"chat_id": msg.ChatID, "issue": link.IssueURL, "error": detail},
	})
	link.ReceiptStatus, link.ReceiptError = status, detail
}

func formatID(id int64) string {
	return fmt.Sprintf("%d", id)
}

// dispatch 尽力把任务投给 pipeline-lead；失败只记录在链路上，不影响建单结果。
func (s *Service) dispatch(ctx context.Context, link *store.BridgeLink, project *store.Project, title string) *store.BridgeLink {
	if s.dispatcher == nil {
		_ = s.store.MarkDispatched(ctx, link.ID, "", "skipped", "no dispatcher configured")
		return s.reload(ctx, link)
	}
	agentID, err := s.dispatcher.Dispatch(ctx, link, project, title)
	status, detail := "sent", ""
	switch {
	case errors.Is(err, ErrNoBinding):
		status, detail = "unbound", "项目未绑定 pipeline-lead"
	case err != nil:
		status, detail = "failed", err.Error()
	}
	_ = s.store.MarkDispatched(ctx, link.ID, agentID, status, detail)
	_ = s.store.RecordDispatch(ctx, store.Dispatch{LinkID: link.ID, AgentID: agentID, Status: status, Message: detail})
	return s.reload(ctx, link)
}

func (s *Service) reload(ctx context.Context, link *store.BridgeLink) *store.BridgeLink {
	if updated, err := s.store.GetBridgeLink(ctx, link.ID); err == nil {
		return updated
	}
	return link
}

func (s *Service) resolveTarget(ctx context.Context, projectID *string) (*store.Project, string, []string, error) {
	if projectID == nil {
		return nil, "", nil, fmt.Errorf("%w: project_id is required to resolve a repository", ErrInvalidMessage)
	}
	project, err := s.store.GetProject(ctx, *projectID)
	if err != nil {
		return nil, "", nil, fmt.Errorf("bridge: load project: %w", err)
	}
	if !project.Enabled {
		return nil, "", nil, fmt.Errorf("bridge: project %s is disabled", project.ID)
	}
	repo := normalizeRepoRef(project.GitHubIssueRepo)
	if repo == "" {
		repo = normalizeRepoRef(project.GitHubRepoURL)
	}
	if repo == "" {
		return nil, "", nil, fmt.Errorf("bridge: project %s has no GitHub repository", project.ID)
	}
	prefix := strings.TrimSpace(project.LabelsPrefix)
	if prefix == "" {
		prefix = "smartpipe"
	}
	return project, repo, []string{"source:bluex", prefix}, nil
}

// normalizeRepoRef 把完整仓库地址归一成 owner/repo：
// https://github.com/owner/repo.git、git@github.com:owner/repo.git、owner/repo 都接受。
func normalizeRepoRef(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		s = u.Path
	} else if i := strings.Index(s, ":"); i > 0 && strings.Contains(s[:i], "@") {
		s = s[i+1:]
	}
	s = strings.Trim(strings.TrimSuffix(strings.Trim(s, "/"), ".git"), "/")
	return s
}

// ClientFromCredentials 从凭据库解析可用的 GitHub 客户端：
// 优先项目级 github_app → 项目级 github_pat → 全局 github_app → 全局 github_pat。
func ClientFromCredentials(ctx context.Context, st *store.Store, projectID *string) (*github.Client, error) {
	all, err := st.ListCredentials(ctx, nil)
	if err != nil {
		return nil, err
	}
	byKind := func(kind string) []store.Credential {
		out := make([]store.Credential, 0, len(all))
		for _, c := range all {
			if !c.Enabled || c.Kind != kind {
				continue
			}
			if projectID != nil && c.ProjectID != nil && *c.ProjectID == *projectID {
				out = append(out, c)
			}
		}
		return out
	}
	globalByKind := func(kind string) []store.Credential {
		out := make([]store.Credential, 0, len(all))
		for _, c := range all {
			if c.Enabled && c.Kind == kind && c.ProjectID == nil {
				out = append(out, c)
			}
		}
		return out
	}

	candidates := append(byKind(store.CredentialGitHubApp), globalByKind(store.CredentialGitHubApp)...)
	candidates = append(candidates, byKind(store.CredentialGitHubPAT)...)
	candidates = append(candidates, globalByKind(store.CredentialGitHubPAT)...)
	if len(candidates) == 0 {
		return nil, errors.New("bridge: no enabled GitHub credential (github_app or github_pat)")
	}

	var lastErr error
	for _, c := range candidates {
		kind, secret, meta, err := st.GetSecret(ctx, c.ID)
		if err != nil {
			lastErr = err
			continue
		}
		apiBase := metaString(meta, "api_base", github.DefaultAPIBase)
		if kind == store.CredentialGitHubPAT {
			return github.New(apiBase, secret), nil
		}
		appID := metaString(meta, "app_id", "")
		installID := metaString(meta, "installation_id", "")
		if appID == "" || installID == "" {
			lastErr = errors.New("bridge: github_app credential missing app_id/installation_id metadata")
			continue
		}
		token, _, err := github.InstallationToken(ctx, apiBase, appID, installID, secret, nowFunc())
		if err != nil {
			lastErr = err
			continue
		}
		return github.New(apiBase, token), nil
	}
	return nil, fmt.Errorf("bridge: no usable GitHub credential: %w", lastErr)
}

func metaString(meta map[string]any, key, fallback string) string {
	if meta == nil {
		return fallback
	}
	if v, ok := meta[key].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}
