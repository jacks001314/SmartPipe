// Package bridge 实现蓝信消息 ↔ GitHub Issue 的桥接：
// 入站消息去重、建 Issue、写映射表；Webhook 验签、去重并记录事件。
package bridge

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

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
	store     *store.Store
	clientFor func(ctx context.Context, projectID *string, repo string) (*github.Client, error)
	notifier  Notifier
}

// NewService 构造桥接服务；clientFor 为 nil 时使用凭据库解析。
func NewService(st *store.Store, clientFor func(context.Context, *string, string) (*github.Client, error)) *Service {
	if clientFor == nil {
		clientFor = func(ctx context.Context, projectID *string, repo string) (*github.Client, error) {
			return ClientForRepo(ctx, st, projectID, repo)
		}
	}
	return &Service{store: st, clientFor: clientFor}
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
	_, repo, labels, err := s.resolveTarget(ctx, projectID)
	if err != nil {
		return nil, false, err
	}

	client, err := s.clientFor(ctx, projectID, repo)
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
	return ClientForRepo(ctx, st, projectID, "")
}

// ClientForRepo 在 ClientFromCredentials 的基础上确认凭据真能访问 repo。
// GitHub App 只安装在部分账号上，安装范围之外的仓库一律返回 404（即使仓库
// 本身对用户可见）。此时按候选顺序回退到下一份凭据（通常是用户 PAT），
// 避免整个项目因为优先级最高的凭据看不到仓库而整条链路报错。
func ClientForRepo(ctx context.Context, st *store.Store, projectID *string, repo string) (*github.Client, error) {
	candidates, err := githubCredentialCandidates(ctx, st, projectID)
	if err != nil {
		return nil, err
	}
	repo = normalizeRepoRef(repo)
	// 只有一个候选时没有回退余地，省掉一次探测请求。
	probe := repo != "" && len(candidates) > 1

	var lastErr error
	for _, c := range candidates {
		client, err := clientFromCredential(ctx, st, c)
		if err != nil {
			lastErr = err
			continue
		}
		if !probe {
			return client, nil
		}
		if err := repoAccessible(ctx, projectID, c.ID, repo, client); err != nil {
			lastErr = fmt.Errorf("credential %s cannot access %s: %w", c.Name, repo, err)
			continue
		}
		return client, nil
	}
	return nil, fmt.Errorf("bridge: no usable GitHub credential: %w", lastErr)
}

// githubCredentialCandidates 按优先级列出可用的 GitHub 凭据：
// 项目级 github_app → 项目级 github_pat → 全局 github_app → 全局 github_pat。
func githubCredentialCandidates(ctx context.Context, st *store.Store, projectID *string) ([]store.Credential, error) {
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
	return candidates, nil
}

// clientFromCredential 把一条凭据解析成 GitHub 客户端；App 凭据会换取安装令牌，
// 令牌在有效期内复用，避免每次调用都多打一次 GitHub 接口。
func clientFromCredential(ctx context.Context, st *store.Store, c store.Credential) (*github.Client, error) {
	kind, secret, meta, err := st.GetSecret(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	apiBase := metaString(meta, "api_base", github.DefaultAPIBase)
	if kind == store.CredentialGitHubPAT {
		return github.New(apiBase, secret), nil
	}
	appID := metaString(meta, "app_id", "")
	installID := metaString(meta, "installation_id", "")
	if appID == "" || installID == "" {
		return nil, errors.New("bridge: github_app credential missing app_id/installation_id metadata")
	}
	if token, ok := cachedInstallationToken(c.ID); ok {
		return github.New(apiBase, token), nil
	}
	token, expires, err := github.InstallationToken(ctx, apiBase, appID, installID, secret, nowFunc())
	if err != nil {
		return nil, err
	}
	rememberInstallationToken(c.ID, token, expires)
	return github.New(apiBase, token), nil
}

type repoAccessEntry struct {
	err     error
	expires time.Time
}

// repoAccessCache 记住某条凭据对某个仓库的可见性，避免每次调用都探测。
var repoAccessCache sync.Map

// repoAccessible 检查凭据能否访问仓库；成功缓存 10 分钟，失败缓存 1 分钟。
func repoAccessible(ctx context.Context, projectID *string, credID, repo string, client *github.Client) error {
	key := credentialRepoKey(projectID, credID, repo)
	if v, ok := repoAccessCache.Load(key); ok {
		if e, ok := v.(repoAccessEntry); ok && nowFunc().Before(e.expires) {
			return e.err
		}
	}
	err := client.CanAccessRepo(ctx, repo)
	ttl := 10 * time.Minute
	if err != nil {
		ttl = time.Minute
	}
	repoAccessCache.Store(key, repoAccessEntry{err: err, expires: nowFunc().Add(ttl)})
	return err
}

func credentialRepoKey(projectID *string, credID, repo string) string {
	project := ""
	if projectID != nil {
		project = *projectID
	}
	return project + "|" + credID + "|" + strings.ToLower(repo)
}

// ResetCredentialCache 清空凭据相关的内存缓存，凭据变更后立即生效。
func ResetCredentialCache() {
	repoAccessCache.Range(func(k, _ any) bool {
		repoAccessCache.Delete(k)
		return true
	})
	installationTokenCache.Range(func(k, _ any) bool {
		installationTokenCache.Delete(k)
		return true
	})
}

type installationTokenEntry struct {
	token   string
	expires time.Time
}

var installationTokenCache sync.Map

func cachedInstallationToken(credID string) (string, bool) {
	v, ok := installationTokenCache.Load(credID)
	if !ok {
		return "", false
	}
	entry, ok := v.(installationTokenEntry)
	if !ok {
		return "", false
	}
	// 过期前 5 分钟续签，留出请求耗时的余量。
	if !entry.expires.IsZero() && nowFunc().Add(5*time.Minute).After(entry.expires) {
		installationTokenCache.Delete(credID)
		return "", false
	}
	return entry.token, true
}

func rememberInstallationToken(credID, token string, expires time.Time) {
	if strings.TrimSpace(token) == "" {
		return
	}
	installationTokenCache.Store(credID, installationTokenEntry{token: token, expires: expires})
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
