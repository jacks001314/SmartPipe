package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/release"
	"smartpipe/backend/internal/store"
)

type releaseRequest struct {
	ProjectID     string   `json:"project_id"`
	Repo          string   `json:"repo"`
	Version       string   `json:"version"`
	Commit        string   `json:"commit"`
	ArtifactGlobs []string `json:"artifact_globs"`
	Notes         string   `json:"notes"`
}

func (a *API) registerReleases(p iris.Party) {
	p = p.Party("/releases")
	p.Get("", a.listReleases)
	p.Get("/{id}", a.getRelease)
	p.Post("", a.createRelease)
	a.registerReleaseRuns(p)
	a.registerReleaseV2(p)
}

func (a *API) listReleases(c iris.Context) {
	items, err := a.store.ListReleases(c.Request().Context(), intQuery(c, "limit", 50))
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) getRelease(c iris.Context) {
	id, err := strconv.ParseInt(c.Params().Get("id"), 10, 64)
	if err != nil {
		a.badRequest(c, "非法 id")
		return
	}
	item, err := a.store.GetRelease(c.Request().Context(), id)
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: item})
}

func (a *API) createRelease(c iris.Context) {
	if strings.TrimSpace(a.releaseURL) == "" {
		c.StatusCode(http.StatusServiceUnavailable)
		_ = c.JSON(envelope{Err: &apiError{Code: "release_not_configured", Message: "未配置 SMARTPIPE_RELEASE_URL"}})
		return
	}
	var in releaseRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	if strings.TrimSpace(in.Version) == "" || strings.TrimSpace(in.Commit) == "" {
		a.badRequest(c, "version 与 commit 必填")
		return
	}
	var projectID *string
	repo := strings.TrimSpace(in.Repo)
	if strings.TrimSpace(in.ProjectID) != "" {
		project, err := a.store.GetProject(c.Request().Context(), in.ProjectID)
		if err != nil {
			a.fail(c, err)
			return
		}
		v := project.ID
		projectID = &v
		if repo == "" {
			repo = strings.TrimSpace(project.GitHubRepoURL)
		}
	}
	if repo == "" {
		a.badRequest(c, "repo 必填（或为项目配置 github_repo_url）")
		return
	}
	payload := map[string]any{
		"repo": repo, "commit": in.Commit, "version": in.Version,
		"artifact_globs": in.ArtifactGlobs, "notes": in.Notes,
	}
	raw, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(c.Request().Context(), 40*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.releaseURL, "/")+"/build", bytes.NewReader(raw))
	if err != nil {
		a.badRequest(c, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if a.releaseToken != "" {
		req.Header.Set("X-Release-Token", a.releaseToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.StatusCode(http.StatusBadGateway)
		_ = c.JSON(envelope{Err: &apiError{Code: "release_unreachable", Message: err.Error()}})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 500 {
		c.StatusCode(http.StatusBadGateway)
		_ = c.JSON(envelope{Err: &apiError{Code: "release_error", Message: strings.TrimSpace(string(body))}})
		return
	}
	var report release.Report
	if err := json.Unmarshal(body, &report); err != nil {
		c.StatusCode(http.StatusBadGateway)
		_ = c.JSON(envelope{Err: &apiError{Code: "release_bad_response", Message: err.Error()}})
		return
	}
	manifest, _ := json.Marshal(report.Manifest)
	artifacts, _ := json.Marshal(report.Artifacts)
	status := "failed"
	if report.OK {
		status = "built"
	}
	record, err := a.store.CreateRelease(c.Request().Context(), store.Release{
		ProjectID: projectID, Version: report.Version, CommitSHA: report.Commit, OK: report.OK,
		Status: status, Manifest: manifest, ManifestSHA256: report.ManifestSHA256,
		Signature: report.Signature, Artifacts: artifacts, OutputDir: report.OutputDir,
		Error: report.Error, DurationMS: int(report.DurationMS),
	})
	if err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "release.build", "release", strconv.FormatInt(record.ID, 10), projectID, status,
		map[string]any{"version": report.Version, "commit": report.Commit, "manifest_sha256": report.ManifestSHA256})
	_ = c.JSON(envelope{Data: iris.Map{"record": record, "report": report}})
}
