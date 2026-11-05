package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/gate"
	"smartpipe/backend/internal/store"
)

type verifyRequest struct {
	ProjectID   string            `json:"project_id"`
	IssueRef    string            `json:"issue_ref"`
	Repo        string            `json:"repo"`
	BaseCommit  string            `json:"base_commit"`
	Patch       string            `json:"patch"`
	TestLog     string            `json:"test_log"`
	Author      string            `json:"author"`
	Environment map[string]string `json:"environment"`
}

type verifyReport struct {
	OK           bool              `json:"ok"`
	Error        string            `json:"error"`
	IssueID      string            `json:"issue_id"`
	PatchHash    string            `json:"patch_hash"`
	BaseCommit   string            `json:"base_commit"`
	HeadCommit   string            `json:"head_commit"`
	Files        []string          `json:"files"`
	ChangedLines int               `json:"changed_lines"`
	Checks       []json.RawMessage `json:"checks"`
	Environment  map[string]string `json:"environment"`
	DurationMS   int64             `json:"duration_ms"`
	WorkDir      string            `json:"work_dir"`
	VerifiedAt   string            `json:"verified_at"`
}

func (a *API) registerVerification(p iris.Party) {
	p = p.Party("/verifications")
	p.Get("", a.listVerifications)
	p.Get("/{id}", a.getVerification)
	p.Post("", a.createVerification)
}

func (a *API) listAttempts(c iris.Context) {
	if ref := strings.TrimSpace(c.URLParam("issue_ref")); ref != "" {
		item, err := a.store.GetIssueAttempt(c.Request().Context(), ref)
		if err != nil {
			a.fail(c, err)
			return
		}
		_ = c.JSON(envelope{Data: item})
		return
	}
	items, err := a.store.ListIssueAttempts(c.Request().Context(), intQuery(c, "limit", 50))
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) listVerifications(c iris.Context) {
	items, err := a.store.ListVerifications(c.Request().Context(), intQuery(c, "limit", 50))
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) getVerification(c iris.Context) {
	id, err := strconv.ParseInt(c.Params().Get("id"), 10, 64)
	if err != nil {
		a.badRequest(c, "非法 id")
		return
	}
	item, err := a.store.GetVerification(c.Request().Context(), id)
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: item})
}

func (a *API) createVerification(c iris.Context) {
	if strings.TrimSpace(a.verifyURL) == "" {
		c.StatusCode(http.StatusServiceUnavailable)
		_ = c.JSON(envelope{Err: &apiError{Code: "verifier_not_configured", Message: "未配置 SMARTPIPE_VERIFY_URL，无法执行补丁验证"}})
		return
	}
	var in verifyRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	if strings.TrimSpace(in.Repo) == "" || strings.TrimSpace(in.Patch) == "" || strings.TrimSpace(in.BaseCommit) == "" {
		a.badRequest(c, "repo、base_commit、patch 必填")
		return
	}
	var (
		projectID *string
		project   *store.Project
	)
	if strings.TrimSpace(in.ProjectID) != "" {
		loaded, err := a.store.GetProject(c.Request().Context(), in.ProjectID)
		if err != nil {
			a.fail(c, err)
			return
		}
		project = loaded
		v := in.ProjectID
		projectID = &v
	}
	if strings.TrimSpace(in.IssueRef) != "" {
		state, gerr := gate.Check(c.Request().Context(), a.store, project, in.IssueRef)
		if gerr == nil && !state.OK {
			a.audit(c.Request().Context(), c, "patch.verify", "issue", in.IssueRef, projectID, "blocked",
				map[string]any{"reason": state.Reason, "version": state.Version})
			c.StatusCode(http.StatusForbidden)
			_ = c.JSON(envelope{Err: &apiError{Code: "approval_required", Message: state.Reason}})
			return
		}
	}

	payload := map[string]any{
		"repo": in.Repo, "issue_id": in.IssueRef, "base_commit": in.BaseCommit,
		"patch": in.Patch, "test_log": in.TestLog, "author": in.Author, "environment": in.Environment,
	}
	raw, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(c.Request().Context(), 35*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.verifyURL, "/")+"/verify", bytes.NewReader(raw))
	if err != nil {
		a.badRequest(c, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if a.verifyToken != "" {
		req.Header.Set("X-Verify-Token", a.verifyToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.StatusCode(http.StatusBadGateway)
		_ = c.JSON(envelope{Err: &apiError{Code: "verifier_unreachable", Message: err.Error()}})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 500 {
		c.StatusCode(http.StatusBadGateway)
		_ = c.JSON(envelope{Err: &apiError{Code: "verifier_error", Message: strings.TrimSpace(string(body))}})
		return
	}
	var report verifyReport
	if err := json.Unmarshal(body, &report); err != nil {
		c.StatusCode(http.StatusBadGateway)
		_ = c.JSON(envelope{Err: &apiError{Code: "verifier_bad_response", Message: err.Error()}})
		return
	}
	files, _ := json.Marshal(report.Files)
	checks, _ := json.Marshal(report.Checks)
	record := store.PatchVerification{
		ProjectID: projectID, IssueRef: in.IssueRef, Repo: in.Repo,
		BaseCommit: report.BaseCommit, HeadCommit: report.HeadCommit, PatchHash: report.PatchHash,
		OK: report.OK, Files: files, ChangedLines: report.ChangedLines, Checks: checks,
		Error: report.Error, DurationMS: int(report.DurationMS), Report: body,
	}
	if record.BaseCommit == "" {
		record.BaseCommit = in.BaseCommit
	}
	stored, err := a.store.CreateVerification(c.Request().Context(), record)
	if err != nil {
		a.fail(c, err)
		return
	}
	var attempt *store.IssueAttempt
	if strings.TrimSpace(in.IssueRef) != "" {
		attempt, _ = a.store.RecordIssueAttempt(c.Request().Context(), strings.TrimSpace(in.IssueRef), projectID, stored.ID, report.OK)
	}
	result := "failed"
	if report.OK {
		result = "ok"
	}
	a.audit(c.Request().Context(), c, "patch.verify", "verification", fmt.Sprintf("%d", stored.ID), projectID, result,
		map[string]any{"repo": in.Repo, "issue": in.IssueRef, "patch_hash": report.PatchHash, "files": report.Files})
	_ = c.JSON(envelope{Data: iris.Map{"record": stored, "report": report, "attempt": attempt}})
}
