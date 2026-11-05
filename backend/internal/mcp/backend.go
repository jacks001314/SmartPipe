package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AdminBackend 通过管理接口执行工具调用；管理员令牌只存在于网关进程内。
type AdminBackend struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewAdminBackend 构造后端客户端。
func NewAdminBackend(baseURL, token string) *AdminBackend {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = "http://127.0.0.1:8092"
	}
	return &AdminBackend{BaseURL: base, Token: strings.TrimSpace(token), HTTP: &http.Client{Timeout: 65 * time.Minute}}
}

func (b *AdminBackend) projectID(p *Principal, args map[string]any) (string, error) {
	argID, _ := args["project_id"].(string)
	argID = strings.TrimSpace(argID)
	if p.ProjectID != nil && strings.TrimSpace(*p.ProjectID) != "" {
		own := strings.TrimSpace(*p.ProjectID)
		if argID != "" && argID != own {
			return "", fmt.Errorf("project_id %s 不在该令牌的授权范围内", argID)
		}
		return own, nil
	}
	if argID == "" {
		return "", fmt.Errorf("project_id 必填（令牌未绑定项目）")
	}
	return argID, nil
}

func intArg(args map[string]any, key string) (int64, error) {
	switch v := args[key].(type) {
	case float64:
		return int64(v), nil
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case json.Number:
		return v.Int64()
	default:
		return 0, fmt.Errorf("参数 %s 必须是整数", key)
	}
}

func strArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func strList(args map[string]any, key string) []string {
	raw, ok := args[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

// Call 执行工具。
func (b *AdminBackend) Call(ctx context.Context, p *Principal, tool string, args map[string]any) (any, error) {
	projectID, err := b.projectID(p, args)
	if err != nil {
		return nil, err
	}
	switch tool {
	case "project.info":
		return b.get(ctx, "/admin/projects/"+url.PathEscape(projectID))
	case "issue.get":
		number, err := intArg(args, "number")
		if err != nil {
			return nil, err
		}
		return b.get(ctx, fmt.Sprintf("/admin/issues/%d?project_id=%s", number, url.QueryEscape(projectID)))
	case "issue.comment":
		number, err := intArg(args, "number")
		if err != nil {
			return nil, err
		}
		body := strArg(args, "body")
		if body == "" {
			return nil, fmt.Errorf("body 必填")
		}
		return b.post(ctx, fmt.Sprintf("/admin/issues/%d/comments", number), map[string]any{"project_id": projectID, "body": body})
	case "issue.add_labels":
		number, err := intArg(args, "number")
		if err != nil {
			return nil, err
		}
		labels := strList(args, "labels")
		if len(labels) == 0 {
			return nil, fmt.Errorf("labels 必填")
		}
		return b.post(ctx, fmt.Sprintf("/admin/issues/%d/labels", number), map[string]any{"project_id": projectID, "labels": labels})
	case "release.candidates":
		return b.get(ctx, "/admin/releases/candidates?project_id="+url.QueryEscape(projectID))
	case "verify.patch":
		payload := map[string]any{
			"project_id": projectID, "issue_ref": strArg(args, "issue_ref"), "repo": strArg(args, "repo"),
			"base_commit": strArg(args, "base_commit"), "patch": args["patch"],
			"test_log": strArg(args, "test_log"), "author": strArg(args, "author"),
		}
		if env, ok := args["environment"].(map[string]any); ok {
			payload["environment"] = env
		}
		if payload["patch"] == nil {
			return nil, fmt.Errorf("patch 必填")
		}
		return b.post(ctx, "/admin/verifications", payload)
	case "repo.commit":
		payload := map[string]any{
			"project_id": projectID, "issue_ref": strArg(args, "issue_ref"), "repo": strArg(args, "repo"),
			"base_commit": strArg(args, "base_commit"), "branch": strArg(args, "branch"),
			"message": strArg(args, "message"), "target_branch": strArg(args, "target_branch"),
			"author_name": strArg(args, "author_name"), "author_email": strArg(args, "author_email"),
		}
		if patch, ok := args["patch"]; ok {
			payload["patch"] = patch
		} else {
			return nil, fmt.Errorf("patch 必填")
		}
		return b.post(ctx, "/admin/commits", payload)
	case "repo.push":
		return b.post(ctx, "/admin/pushes", map[string]any{
			"project_id": projectID, "issue_ref": strArg(args, "issue_ref"), "repo": strArg(args, "repo"),
			"branch": strArg(args, "branch"), "remote": strArg(args, "remote"),
		})
	case "derivation.create":
		return b.post(ctx, "/admin/derivations", map[string]any{
			"project_id": projectID, "issue_ref": strArg(args, "issue_ref"),
			"agent_name": strArg(args, "agent_name"), "display_name": strArg(args, "display_name"),
			"template_name": strArg(args, "template_name"),
		})
	case "release.run":
		payload := map[string]any{
			"project_id": projectID, "version": strArg(args, "version"), "commit": strArg(args, "commit"),
			"branch": strArg(args, "branch"), "notes": strArg(args, "notes"),
			"dry_run": args["dry_run"] == true,
		}
		if globs := strList(args, "artifact_globs"); len(globs) > 0 {
			payload["artifact_globs"] = globs
		}
		return b.post(ctx, "/admin/releases/runs", payload)
	case "release.rollback":
		runID, err := intArg(args, "run_id")
		if err != nil {
			return nil, err
		}
		return b.post(ctx, fmt.Sprintf("/admin/releases/runs/%d/rollback", runID), map[string]any{})
	default:
		return nil, fmt.Errorf("unknown tool %q", tool)
	}
}

func (b *AdminBackend) get(ctx context.Context, path string) (any, error) {
	return b.do(ctx, http.MethodGet, path, nil)
}

func (b *AdminBackend) post(ctx context.Context, path string, payload any) (any, error) {
	return b.do(ctx, http.MethodPost, path, payload)
}

func (b *AdminBackend) do(ctx context.Context, method, path string, payload any) (any, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if b.Token != "" {
		req.Header.Set("Authorization", "Bearer "+b.Token)
	}
	client := b.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("admin-api 不可达: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var envelope struct {
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("admin-api 返回非 JSON（HTTP %d）", resp.StatusCode)
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("%s: %s", envelope.Error.Code, envelope.Error.Message)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("admin-api HTTP %d", resp.StatusCode)
	}
	var out any
	if len(envelope.Data) > 0 {
		_ = json.Unmarshal(envelope.Data, &out)
	}
	return out, nil
}
