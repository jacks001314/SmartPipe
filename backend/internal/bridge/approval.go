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
	// GroupNotify 把方案登记与批准两个里程碑同步到项目绑定的蓝信群；nil 表示不通知。
	GroupNotify Notifier
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
		s.notifyGroups(ctx, project, planRegisteredText(ev.IssueNumber, plan.Version, issueURL(ev, repo)))
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
		s.notifyGroups(ctx, project, planApprovedText(ev.IssueNumber, version, approval.Approver, issueURL(ev, repo)))
		_ = s.Store.AppendAudit(ctx, store.AuditEntry{
			Actor: ev.User, Action: "issue.plan.approve", TargetType: "issue",
			TargetID: fmt.Sprintf("%s#%d", repo, ev.IssueNumber), ProjectID: projectIDPtr(project),
			Result: "ok", Detail: map[string]any{"version": version, "hash": actual},
		})

		return ApprovalOutcome{Action: "approved", Version: version, Hash: actual}, nil
	}
	return ApprovalOutcome{Action: "ignored"}, nil
}

// planRegisteredText 与 planApprovedText 是发给蓝信群的里程碑文本。
func planRegisteredText(issue int, version int, url string) string {
	return fmt.Sprintf("【SmartPipe】Issue #%d 已提出方案 v%d，等待维护者评论 /approve v%d。\n%s", issue, version, version, url)
}

func planApprovedText(issue int, version int, approver, url string) string {
	return fmt.Sprintf("【SmartPipe】Issue #%d 方案 v%d 已获 @%s 批准，开始开发。\n%s", issue, version, approver, url)
}

// notifyGroups 按项目绑定的群发送里程碑消息；发送失败不影响主流程。
func (s *ApprovalService) notifyGroups(ctx context.Context, project *store.Project, text string) {
	if s.GroupNotify == nil || project == nil {
		return
	}
	for _, gid := range strings.Split(project.BluexGroupIDs, ",") {
		gid = strings.TrimSpace(gid)
		if gid == "" {
			continue
		}
		_ = s.GroupNotify.Reply(ctx, project.LanxinBotID, "group", gid, "", text)
	}
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
	if s.ClientFor == nil {
		client, err := ClientForRepo(ctx, s.Store, &project.ID, repo)
		if err != nil {
			return nil, project, err
		}
		return client, project, nil
	}
	client, err := s.ClientFor(ctx, &project.ID)
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
