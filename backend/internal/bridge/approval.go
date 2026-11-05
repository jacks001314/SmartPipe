package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"smartpipe/backend/internal/github"
	"smartpipe/backend/internal/store"
)

// ErrNotMaintainer 表示评论人没有批准权限。
var ErrNotMaintainer = errors.New("bridge: commenter is not a maintainer")

var approvePattern = regexp.MustCompile(`(?i)^/approve\s+v(\d+)\s*$`)

// CommentEvent 是 issue_comment webhook 的归一化输入。
type CommentEvent struct {
	Repo         string
	IssueNumber  int
	CommentID    int64
	Body         string
	User         string
	Association  string
	IssueHTMLURL string
}

// ApprovalOutcome 描述一次评论处理的结果，用于 webhook 响应与审计。
type ApprovalOutcome struct {
	Action  string `json:"action"`
	Version int    `json:"version,omitempty"`
	Hash    string `json:"hash,omitempty"`
	Message string `json:"message,omitempty"`
}

// ApprovalService 处理 /plan 与 /approve 命令。
type ApprovalService struct {
	Store     *store.Store
	ClientFor func(ctx context.Context, projectID *string) (*github.Client, error)
	// NotifyLead 在批准后通知 pipeline-lead；nil 表示只记录。
	NotifyLead func(ctx context.Context, projectID, text string) error
}

// NormalizePlan 统一换行与首尾空白，保证哈希稳定。
func NormalizePlan(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	return strings.TrimSpace(body)
}

// PlanHash 返回方案内容的 sha256 指纹，格式 sha256:<hex>。
func PlanHash(body string) string {
	sum := sha256.Sum256([]byte(NormalizePlan(body)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// IsMaintainer 判断 GitHub 的 author_association 是否具备批准权限。
func IsMaintainer(assoc string) bool {
	switch strings.ToUpper(strings.TrimSpace(assoc)) {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	default:
		return false
	}
}

// HandleComment 处理一条 issue_comment。非命令返回 action=ignored。
func (s *ApprovalService) HandleComment(ctx context.Context, ev CommentEvent) (ApprovalOutcome, error) {
	body := strings.TrimSpace(ev.Body)
	lower := strings.ToLower(body)
	repo := strings.TrimSpace(ev.Repo)
	if repo == "" || ev.IssueNumber <= 0 {
		return ApprovalOutcome{Action: "ignored"}, nil
	}
	client, project, err := s.clientForRepo(ctx, repo)
	if err != nil {
		return ApprovalOutcome{Action: "ignored"}, err
	}

	switch {
	case strings.HasPrefix(lower, "/plan"):
		planText := strings.TrimSpace(body[len("/plan"):])
		if planText == "" {
			s.reply(ctx, client, repo, ev.IssueNumber, "@"+ev.User+" 请在 /plan 后附上方案内容。")
			return ApprovalOutcome{Action: "plan_empty"}, nil
		}
		hash := PlanHash(planText)
		plan, err := s.Store.CreatePlan(ctx, repo, ev.IssueNumber, planText, hash, ev.User, ev.CommentID)
		if err != nil {
			return ApprovalOutcome{Action: "plan_failed"}, err
		}
		msg := fmt.Sprintf("方案 v%d 已登记，内容指纹 `%s`。\n\n请维护者审阅后评论 `/approve v%d`；方案内容再修改会自动生成新版本，旧批准不生效。",
			plan.Version, shortHash(hash), plan.Version)
		s.reply(ctx, client, repo, ev.IssueNumber, msg)
		_ = client.AddLabels(ctx, repo, ev.IssueNumber, []string{"needs-approval"})
		_ = s.Store.AppendAudit(ctx, store.AuditEntry{
			Actor: ev.User, Action: "issue.plan.create", TargetType: "issue",
			TargetID: fmt.Sprintf("%s#%d", repo, ev.IssueNumber), ProjectID: projectIDPtr(project),
			Result: "ok", Detail: map[string]any{"version": plan.Version, "hash": hash},
		})
		return ApprovalOutcome{Action: "plan_created", Version: plan.Version, Hash: hash}, nil

	case approvePattern.MatchString(body):
		if !IsMaintainer(ev.Association) {
			s.reply(ctx, client, repo, ev.IssueNumber,
				fmt.Sprintf("@%s 没有审批权限：需要仓库 OWNER / MEMBER / COLLABORATOR。", ev.User))
			return ApprovalOutcome{Action: "approve_denied"}, ErrNotMaintainer
		}
		m := approvePattern.FindStringSubmatch(body)
		var version int
		fmt.Sscanf(m[1], "%d", &version)
		plan, err := s.Store.GetPlan(ctx, repo, ev.IssueNumber, version)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				s.reply(ctx, client, repo, ev.IssueNumber,
					fmt.Sprintf("找不到方案 v%d。请先由 lead 用 /plan 提交方案。", version))
				return ApprovalOutcome{Action: "approve_missing_plan", Version: version}, nil
			}
			return ApprovalOutcome{Action: "approve_failed"}, err
		}
		actual := PlanHash(plan.Body)
		if actual != plan.ContentHash {
			s.reply(ctx, client, repo, ev.IssueNumber,
				fmt.Sprintf("方案 v%d 的内容与登记指纹不一致，批准已拒绝。请重新提交 /plan。", version))
			return ApprovalOutcome{Action: "approve_hash_mismatch", Version: version}, errors.New("bridge: plan hash mismatch")
		}
		approval, err := s.Store.ApprovePlan(ctx, store.IssueApproval{
			Repo: repo, IssueNumber: ev.IssueNumber, Version: version,
			PlanHash: actual, Approver: ev.User, CommentID: ev.CommentID,
		})
		if errors.Is(err, store.ErrDuplicate) {
			s.reply(ctx, client, repo, ev.IssueNumber, fmt.Sprintf("方案 v%d 已经批准过，无需重复。", version))
			return ApprovalOutcome{Action: "approve_duplicate", Version: version, Hash: actual}, nil
		}
		if err != nil {
			return ApprovalOutcome{Action: "approve_failed"}, err
		}
		s.reply(ctx, client, repo, ev.IssueNumber,
			fmt.Sprintf("✅ 方案 v%d 已批准（审批人 @%s，指纹 `%s`）。lead 现在可以开始实现，并在完成后回到本 Issue 汇报。",
				version, approval.Approver, shortHash(actual)))
		_ = client.AddLabels(ctx, repo, ev.IssueNumber, []string{"approved"})
		_ = s.Store.AppendAudit(ctx, store.AuditEntry{
			Actor: ev.User, Action: "issue.plan.approve", TargetType: "issue",
			TargetID: fmt.Sprintf("%s#%d", repo, ev.IssueNumber), ProjectID: projectIDPtr(project),
			Result: "ok", Detail: map[string]any{"version": version, "hash": actual},
		})
		if s.NotifyLead != nil && project != nil {
			text := fmt.Sprintf("Issue %s 的方案 v%d 已获 @%s 批准（指纹 %s）。请开始实现：派生所需 doer/test Agent，完成后在干净副本验证并合并，最后回到 Issue 汇报。",
				issueURL(ev, repo), version, approval.Approver, shortHash(actual))
			if err := s.NotifyLead(ctx, project.ID, text); err != nil {
				return ApprovalOutcome{Action: "approved_notify_failed", Version: version, Hash: actual, Message: err.Error()}, nil
			}
		}
		return ApprovalOutcome{Action: "approved", Version: version, Hash: actual}, nil
	}
	return ApprovalOutcome{Action: "ignored"}, nil
}

func (s *ApprovalService) clientForRepo(ctx context.Context, repo string) (*github.Client, *store.Project, error) {
	projects, err := s.Store.ListProjects(ctx)
	if err != nil {
		return nil, nil, err
	}
	var project *store.Project
	for i := range projects {
		if strings.EqualFold(strings.TrimSpace(projects[i].GitHubIssueRepo), repo) {
			project = &projects[i]
			break
		}
	}
	if project == nil {
		for i := range projects {
			if strings.EqualFold(strings.Trim(strings.TrimSpace(projects[i].GitHubRepoURL), "/"), "https://github.com/"+repo) {
				project = &projects[i]
				break
			}
		}
	}
	if project == nil {
		return nil, nil, fmt.Errorf("bridge: no project configured for %s", repo)
	}
	clientFor := s.ClientFor
	if clientFor == nil {
		clientFor = func(ctx context.Context, projectID *string) (*github.Client, error) {
			return ClientFromCredentials(ctx, s.Store, projectID)
		}
	}
	client, err := clientFor(ctx, &project.ID)
	if err != nil {
		return nil, project, err
	}
	return client, project, nil
}

func (s *ApprovalService) reply(ctx context.Context, client *github.Client, repo string, number int, body string) {
	if client == nil {
		return
	}
	_ = client.Comment(ctx, repo, number, body)
}

func projectIDPtr(p *store.Project) *string {
	if p == nil {
		return nil
	}
	id := p.ID
	return &id
}

func shortHash(hash string) string {
	h := strings.TrimPrefix(hash, "sha256:")
	if len(h) > 12 {
		h = h[:12]
	}
	return h
}

func issueURL(ev CommentEvent, repo string) string {
	if strings.TrimSpace(ev.IssueHTMLURL) != "" {
		return ev.IssueHTMLURL
	}
	return fmt.Sprintf("https://github.com/%s/issues/%d", repo, ev.IssueNumber)
}
