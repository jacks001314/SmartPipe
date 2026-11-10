package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HTTPVCS 走 admin-api 的 /admin/pushes 门禁推送分支。
type HTTPVCS struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

// NewHTTPVCS 构造推送客户端。
func NewHTTPVCS(baseURL, token string) *HTTPVCS {
	return &HTTPVCS{BaseURL: strings.TrimRight(baseURL, "/"), Token: token,
		Client: &http.Client{Timeout: 10 * time.Minute}}
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

// Push 把调度器已经提交好的分支推送到远端，远端地址与凭据由 admin-api 解析。
func (v *HTTPVCS) Push(ctx context.Context, p *Project, w *Worker) (string, error) {
	var out struct {
		OK    bool   `json:"ok"`
		Head  string `json:"commit"`
		Error string `json:"error"`
	}
	if err := v.do(ctx, "/admin/pushes", map[string]any{
		"project_id": p.ID, "issue_ref": fmt.Sprintf("#%d", w.Issue),
		"repo": w.Worktree, "branch": w.Branch,
		// agent/* 分支由机器人独占；远端可能停在失败尝试的提交上，
		// 统一按刚读到的 ref 做 lease 覆盖，避免历史分叉把推送卡死。
		"force_with_lease": true,
	}, &out); err != nil {
		return "", err
	}
	if !out.OK {
		return "", fmt.Errorf("推送被拒绝: %s", out.Error)
	}
	return out.Head, nil
}
