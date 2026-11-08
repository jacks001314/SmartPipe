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

// HTTPVCS 走 admin-api 的 /admin/commits 与 /admin/pushes 门禁。
type HTTPVCS struct {
	BaseURL string
	Token   string
	Client  *http.Client
	GitBin  string
}

// NewHTTPVCS 构造提交/推送客户端。
func NewHTTPVCS(baseURL, token string) *HTTPVCS {
	return &HTTPVCS{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, Client: &http.Client{Timeout: 10 * time.Minute}, GitBin: "git"}
}

func (v *HTTPVCS) do(ctx context.Context, path string, payload any, out any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if v.Token != "" {
		req.Header.Set("Authorization", "Bearer "+v.Token)
	}
	resp, err := v.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env struct {
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &env)
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if env.Error != nil && env.Error.Message != "" {
			msg = env.Error.Message
		}
		return fmt.Errorf("scheduler: %s -> %d: %s", path, resp.StatusCode, msg)
	}
	if out != nil && len(env.Data) > 0 {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

// CommitPush 先把 worktree 复位，再让 admin-api 应用补丁并推送分支。
func (v *HTTPVCS) CommitPush(ctx context.Context, p *Project, w *Worker, message, patch string) (string, error) {
	if strings.TrimSpace(patch) == "" {
		return "", fmt.Errorf("scheduler: empty patch")
	}
	base, err := v.git(ctx, w.Worktree, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if _, err := v.git(ctx, w.Worktree, "reset", "--hard", "HEAD"); err != nil {
		return "", err
	}
	if _, err := v.git(ctx, w.Worktree, "clean", "-fd"); err != nil {
		return "", err
	}
	var commitOut struct {
		OK     bool   `json:"ok"`
		Commit string `json:"commit"`
		Error  string `json:"error"`
	}
	if err := v.do(ctx, "/admin/commits", map[string]any{
		"project_id": p.ID, "issue_ref": fmt.Sprintf("#%d", w.Issue), "repo": w.Worktree,
		"base_commit": base, "branch": w.Branch, "patch": patch, "message": message,
		"author_name": "SmartPipe Codex", "author_email": "codex@smartpipe.local",
	}, &commitOut); err != nil {
		return "", err
	}
	if !commitOut.OK {
		return "", fmt.Errorf("提交被拒绝: %s", commitOut.Error)
	}
	var pushOut struct {
		OK   bool   `json:"ok"`
		Head string `json:"head"`
	}
	if err := v.do(ctx, "/admin/pushes", map[string]any{
		"project_id": p.ID, "issue_ref": fmt.Sprintf("#%d", w.Issue), "repo": w.Worktree, "branch": w.Branch,
	}, &pushOut); err != nil {
		return "", err
	}
	if !pushOut.OK {
		return "", fmt.Errorf("推送失败")
	}
	return commitOut.Commit, nil
}

func (v *HTTPVCS) git(ctx context.Context, dir string, args ...string) (string, error) {
	bin := v.GitBin
	if bin == "" {
		bin = "git"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
