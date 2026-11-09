package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// HTTPVerifier 调 admin-api 的 /admin/verifications，由 patch-verifier 在干净副本上执行。
type HTTPVerifier struct {
	BaseURL string
	Token   string
	Client  *http.Client
	GitBin  string
}

// NewHTTPVerifier 构造验证客户端。
func NewHTTPVerifier(baseURL, token string) *HTTPVerifier {
	return &HTTPVerifier{BaseURL: strings.TrimRight(baseURL, "/"), Token: token,
		Client: &http.Client{Timeout: 40 * time.Minute}, GitBin: "git"}
}

func (v *HTTPVerifier) baseCommit(ctx context.Context, workdir string) (string, error) {
	bin := v.GitBin
	if bin == "" {
		bin = "git"
	}
	cmd := exec.CommandContext(ctx, bin, "rev-parse", "HEAD")
	cmd.Dir = workdir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("scheduler: rev-parse worktree: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Verify 提交补丁并返回验证结果。
func (v *HTTPVerifier) Verify(ctx context.Context, p *Project, workdir, patch string) (bool, string, error) {
	base, err := v.baseCommit(ctx, workdir)
	if err != nil {
		return false, "", err
	}
	repo := p.LocalPath
	if repo == "" {
		repo = p.Repo
	}
	body, _ := json.Marshal(map[string]any{
		"project_id": p.ID, "issue_ref": "", "repo": repo, "base_commit": base,
		"patch": patch, "author": "SmartPipe Codex",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.BaseURL+"/admin/verifications", bytes.NewReader(body))
	if err != nil {
		return false, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if v.Token != "" {
		req.Header.Set("Authorization", "Bearer "+v.Token)
	}
	resp, err := v.Client.Do(req)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var env struct {
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &env)
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if env.Error != nil && env.Error.Message != "" {
			msg = env.Error.Message
		}
		return false, msg, fmt.Errorf("scheduler: verify http %d: %s", resp.StatusCode, msg)
	}
	// admin-api 返回 {"record":..., "report":{...}, "attempt":...}；
	// 早期实现把 data 直接当报告，这里两种形状都兼容。
	var report struct {
		OK               bool            `json:"ok"`
		Error            string          `json:"error"`
		EnvironmentError bool            `json:"environment_error"`
		Checks           json.RawMessage `json:"checks"`
	}
	detail := string(raw)
	if len(env.Data) > 0 {
		payload := env.Data
		var wrapper struct {
			Report json.RawMessage `json:"report"`
		}
		if err := json.Unmarshal(payload, &wrapper); err != nil {
			return false, string(raw), nil
		}
		if len(wrapper.Report) > 0 {
			payload = wrapper.Report
		}
		if err := json.Unmarshal(payload, &report); err != nil {
			return false, string(raw), nil
		}
		detail = string(payload)
	}
	return report.OK, detail, nil
}
