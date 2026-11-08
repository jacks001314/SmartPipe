package scheduler

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

// HTTPTracker 通过 admin-api 访问 GitHub。调度器本身不持有 GitHub 凭据。
type HTTPTracker struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

// NewHTTPTracker 构造 admin-api 客户端。
func NewHTTPTracker(baseURL, token string) *HTTPTracker {
	return &HTTPTracker{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		Client:  &http.Client{Timeout: 60 * time.Second},
	}
}

type apiEnvelope struct {
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (t *HTTPTracker) do(ctx context.Context, method, path string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.BaseURL+path, body)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if t.Token != "" {
		req.Header.Set("Authorization", "Bearer "+t.Token)
	}
	resp, err := t.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var env apiEnvelope
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &env)
	}
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if env.Error != nil && env.Error.Message != "" {
			msg = env.Error.Message
		}
		return fmt.Errorf("scheduler: admin-api %s %s: %d %s", method, path, resp.StatusCode, msg)
	}
	if out != nil {
		if len(env.Data) == 0 {
			return fmt.Errorf("scheduler: empty response from %s", path)
		}
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

// CreateIssue 建 Issue。
func (t *HTTPTracker) CreateIssue(ctx context.Context, projectID, title, body string, labels []string) (Issue, error) {
	var out struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		URL    string `json:"url"`
		State  string `json:"state"`
	}
	err := t.do(ctx, http.MethodPost, "/admin/issues", map[string]any{
		"project_id": projectID, "title": title, "body": body, "labels": labels,
	}, &out)
	if err != nil {
		return Issue{}, err
	}
	return Issue{Number: out.Number, Title: out.Title, URL: out.URL, State: out.State}, nil
}

// GetIssue 读取 Issue。
func (t *HTTPTracker) GetIssue(ctx context.Context, projectID string, number int) (Issue, error) {
	var out struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		State  string `json:"state"`
		URL    string `json:"html_url"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	path := fmt.Sprintf("/admin/issues/%d?project_id=%s", number, url.QueryEscape(projectID))
	if err := t.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return Issue{}, err
	}
	issue := Issue{Number: out.Number, Title: out.Title, Body: out.Body, State: out.State, URL: out.URL}
	for _, l := range out.Labels {
		issue.Labels = append(issue.Labels, l.Name)
	}
	return issue, nil
}

// Comment 写 Issue 评论。
func (t *HTTPTracker) Comment(ctx context.Context, projectID string, number int, body string) error {
	path := fmt.Sprintf("/admin/issues/%d/comments", number)
	return t.do(ctx, http.MethodPost, path, map[string]any{"project_id": projectID, "body": body}, nil)
}

// ListComments 拉 Issue 评论。
func (t *HTTPTracker) ListComments(ctx context.Context, projectID string, number int) ([]Comment, error) {
	var out []struct {
		ID        int64     `json:"id"`
		Author    string    `json:"author"`
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"created_at"`
	}
	path := fmt.Sprintf("/admin/issues/%d/comments?project_id=%s", number, url.QueryEscape(projectID))
	if err := t.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	comments := make([]Comment, 0, len(out))
	for _, c := range out {
		comments = append(comments, Comment{ID: c.ID, Author: c.Author, Body: c.Body, CreatedAt: c.CreatedAt})
	}
	return comments, nil
}

// SearchIssues 查重。
func (t *HTTPTracker) SearchIssues(ctx context.Context, projectID, query string) ([]Issue, error) {
	var out []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		State  string `json:"state"`
		URL    string `json:"url"`
	}
	path := "/admin/issue-search?project_id=" + url.QueryEscape(projectID) + "&q=" + url.QueryEscape(query)
	if err := t.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	issues := make([]Issue, 0, len(out))
	for _, it := range out {
		issues = append(issues, Issue{Number: it.Number, Title: it.Title, State: it.State, URL: it.URL})
	}
	return issues, nil
}

// CreatePR 建 PR。
func (t *HTTPTracker) CreatePR(ctx context.Context, projectID string, issue int, head, base, title, body string) (PullRequest, error) {
	var out struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
		State  string `json:"state"`
	}
	path := fmt.Sprintf("/admin/issues/%d/pulls", issue)
	err := t.do(ctx, http.MethodPost, path, map[string]any{
		"project_id": projectID, "head": head, "base": base, "title": title, "body": body, "draft": true,
	}, &out)
	if err != nil {
		return PullRequest{}, err
	}
	return PullRequest{Number: out.Number, URL: out.URL, State: out.State}, nil
}
