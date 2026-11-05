// Package gate 实现复杂 Issue 的批准门禁：
// 有方案记录的 Issue 必须先有与服务端哈希一致的 /approve 记录，才允许进入实现与派生。
package gate

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"smartpipe/backend/internal/bridge"
	"smartpipe/backend/internal/store"
)

// State 是一次门禁检查的结果。
type State struct {
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issue_number"`
	Required    bool   `json:"required"`
	OK          bool   `json:"ok"`
	Version     int    `json:"version,omitempty"`
	Approved    bool   `json:"approved"`
	Approver    string `json:"approver,omitempty"`
	PlanHash    string `json:"plan_hash,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// ErrApprovalRequired 表示复杂 Issue 尚未获得有效批准。
var ErrApprovalRequired = errors.New("gate: approval required")

// ParseIssueRef 解析 "owner/repo#12"、"#12" 或 "12"。
func ParseIssueRef(ref string) (repo string, number int, ok bool) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return "", 0, false
	}
	hash := strings.LastIndex(trimmed, "#")
	if hash >= 0 {
		repo = strings.TrimSpace(trimmed[:hash])
		num := strings.TrimSpace(trimmed[hash+1:])
		n, err := strconv.Atoi(num)
		if err != nil || n <= 0 {
			return "", 0, false
		}
		return repo, n, true
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return "", n, true
}

// Check 检查 Issue 的批准状态。没有方案记录视为简单 Issue，直接放行。
func Check(ctx context.Context, st *store.Store, project *store.Project, issueRef string) (*State, error) {
	repo, number, ok := ParseIssueRef(issueRef)
	if !ok {
		return nil, fmt.Errorf("gate: invalid issue reference %q", issueRef)
	}
	if repo == "" && project != nil {
		repo = strings.TrimSpace(project.GitHubIssueRepo)
	}
	if repo == "" {
		return nil, fmt.Errorf("gate: issue reference has no repository and project has none")
	}
	state := &State{Repo: repo, IssueNumber: number}
	plan, err := st.LatestPlan(ctx, repo, number)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			state.OK = true
			state.Reason = "no plan recorded; treated as simple issue"
			return state, nil
		}
		return nil, err
	}
	state.Required = true
	state.Version = plan.Version
	state.PlanHash = plan.ContentHash
	if actual := bridge.PlanHash(plan.Body); actual != plan.ContentHash {
		state.Reason = "plan content changed since it was recorded"
		return state, nil
	}
	approval, err := st.GetApproval(ctx, repo, number, plan.Version)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			state.Reason = fmt.Sprintf("plan v%d has no approval yet", plan.Version)
			return state, nil
		}
		return nil, err
	}
	state.Approved = true
	state.Approver = approval.Approver
	if approval.PlanHash != plan.ContentHash {
		state.Approved = false
		state.Reason = "approval hash does not match current plan"
		return state, nil
	}
	state.OK = true
	return state, nil
}
