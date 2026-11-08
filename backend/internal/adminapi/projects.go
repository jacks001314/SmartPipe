package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/store"
)

func (a *API) listProjects(c iris.Context) {
	items, err := a.store.ListProjects(c.Request().Context())
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) getProject(c iris.Context) {
	id := c.Params().Get("id")
	p, err := a.store.GetProject(c.Request().Context(), id)
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: p})
}

func (a *API) createProject(c iris.Context) {
	body, err := c.GetBody()
	if err != nil || len(body) == 0 {
		a.badRequest(c, "请求体为空")
		return
	}
	var in store.Project
	if err := json.Unmarshal(body, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON: "+err.Error())
		return
	}
	// 未显式指定 enabled 时默认启用，避免新建项目处于"停用"状态。
	var probe map[string]any
	_ = json.Unmarshal(body, &probe)
	if _, ok := probe["enabled"]; !ok {
		in.Enabled = true
	}
	if _, ok := probe["codeowners_required"]; !ok {
		in.CodeownersRequired = false
	}
	if in.Name == "" {
		a.badRequest(c, "项目名称不能为空")
		return
	}
	if err := a.validateProjectLanxinSelection(c.Request().Context(), &in, ""); err != nil {
		a.badRequest(c, err.Error())
		return
	}
	p, err := a.store.CreateProject(c.Request().Context(), &in)
	if err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "project.create", "project", p.ID, &p.ID, "ok", map[string]any{"name": p.Name})
	c.StatusCode(http.StatusCreated)
	_ = c.JSON(envelope{Data: p})
}

func (a *API) updateProject(c iris.Context) {
	id := c.Params().Get("id")
	existing, err := a.store.GetProject(c.Request().Context(), id)
	if err != nil {
		a.fail(c, err)
		return
	}
	patch := map[string]any{}
	if err := decodeBody(c, &patch); err != nil {
		a.badRequest(c, "请求体不是合法 JSON: "+err.Error())
		return
	}
	applyProjectPatch(existing, patch)
	if err := a.validateProjectLanxinSelection(c.Request().Context(), existing, existing.ID); err != nil {
		a.badRequest(c, err.Error())
		return
	}
	p, err := a.store.UpdateProject(c.Request().Context(), existing)
	if err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "project.update", "project", p.ID, &p.ID, "ok", map[string]any{"fields": keys(patch)})
	_ = c.JSON(envelope{Data: p})
}

func (a *API) setProjectEnabled(c iris.Context) {
	id := c.Params().Get("id")
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	p, err := a.store.SetProjectEnabled(c.Request().Context(), id, in.Enabled)
	if err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "project.enabled", "project", p.ID, &p.ID, "ok", map[string]any{"enabled": in.Enabled})
	_ = c.JSON(envelope{Data: p})
}

// validateProjectLanxinSelection 在服务端保证项目和机器人/群的绑定有效且不重复。
func (a *API) validateProjectLanxinSelection(ctx context.Context, p *store.Project, currentProjectID string) error {
	groups := splitGroups(p.BluexGroupIDs)
	if len(groups) == 0 {
		return nil
	}
	allGroups, err := a.store.ListLanxinGroups(ctx, p.LanxinBotID, true)
	if err != nil {
		return err
	}
	for _, groupID := range groups {
		found := false
		for _, g := range allGroups {
			if strings.EqualFold(g.GroupID, groupID) && g.Active {
				found = true
				break
			}
		}
		// 兼容迁移期尚未同步群快照的旧项目。
		if !found && p.LanxinBotID != "" {
			return fmt.Errorf("群 %s 不属于所选机器人，或尚未同步到群列表", groupID)
		}
	}
	projects, err := a.store.ListProjects(ctx)
	if err != nil {
		return err
	}
	for _, other := range projects {
		if other.ID == currentProjectID {
			continue
		}
		sameBot := p.LanxinBotID == "" || other.LanxinBotID == "" || p.LanxinBotID == other.LanxinBotID
		if !sameBot {
			continue
		}
		for _, groupID := range groups {
			if containsGroup(other.BluexGroupIDs, groupID) {
				return fmt.Errorf("群 %s 已绑定项目「%s」", groupID, other.Name)
			}
		}
	}
	return nil
}

func applyProjectPatch(p *store.Project, patch map[string]any) {
	str := func(key string, dst *string) {
		if v, ok := patch[key].(string); ok {
			*dst = v
		}
	}
	boolean := func(key string, dst *bool) {
		if v, ok := patch[key].(bool); ok {
			*dst = v
		}
	}
	str("name", &p.Name)
	str("description", &p.Description)
	str("github_provider", &p.GitHubProvider)
	str("github_repo_url", &p.GitHubRepoURL)
	str("github_default_branch", &p.GitHubDefaultBranch)
	str("github_issue_repo", &p.GitHubIssueRepo)
	str("labels_prefix", &p.LabelsPrefix)
	str("approver_teams", &p.ApproverTeams)
	str("verify_profile_path", &p.VerifyProfilePath)
	str("release_profile", &p.ReleaseProfile)
	str("bluex_group_ids", &p.BluexGroupIDs)
	str("lanxin_bot_id", &p.LanxinBotID)
	str("lead_agent_template", &p.LeadAgentTemplate)
	str("codex_model_provider", &p.CodexModelProvider)
	str("codex_model", &p.CodexModel)
	str("codex_reasoning_effort", &p.CodexReasoningEffort)
	str("tool_profile", &p.ToolProfile)
	boolean("codeowners_required", &p.CodeownersRequired)
	boolean("enabled", &p.Enabled)
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
