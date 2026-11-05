package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Release 是 GitHub Release 的响应子集。
type Release struct {
	ID         int64     `json:"id"`
	HTMLURL    string    `json:"html_url"`
	TagName    string    `json:"tag_name"`
	Name       string    `json:"name"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	CreatedAt  time.Time `json:"created_at"`
}

// IsNotFound 判断错误是否为 404。
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// ListIssuesByLabel 列出某个标签下的 open Issue；Pull Request 会被过滤掉。
func (c *Client) ListIssuesByLabel(ctx context.Context, repo, label string, limit int) ([]Issue, error) {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	q := url.Values{}
	q.Set("state", "open")
	q.Set("per_page", strconv.Itoa(limit))
	if label != "" {
		q.Set("labels", label)
	}
	var out []Issue
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/issues?%s", owner, name, q.Encode()), nil, &out); err != nil {
		return nil, err
	}
	issues := make([]Issue, 0, len(out))
	for _, issue := range out {
		if len(issue.PullRequest) > 0 && string(issue.PullRequest) != "null" {
			continue
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

// BranchSHA 返回分支（或任意 ref）指向的提交。
func (c *Client) BranchSHA(ctx context.Context, repo, ref string) (string, error) {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return "", err
	}
	if ref == "" {
		ref = "main"
	}
	var out struct {
		SHA string `json:"sha"`
	}
	path := fmt.Sprintf("/repos/%s/%s/commits/%s", owner, name, url.PathEscape(ref))
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return "", err
	}
	if out.SHA == "" {
		return "", fmt.Errorf("github: branch %s has no commit", ref)
	}
	return out.SHA, nil
}

// TagSHA 读取已存在 tag 指向的提交；tag 不存在时返回空串与 nil。
func (c *Client) TagSHA(ctx context.Context, repo, tag string) (string, error) {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return "", err
	}
	var out struct {
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"object"`
	}
	path := fmt.Sprintf("/repos/%s/%s/git/ref/tags/%s", owner, name, url.PathEscape(tag))
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		if IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	return out.Object.SHA, nil
}

// CreateTagRef 创建轻量 tag。
func (c *Client) CreateTagRef(ctx context.Context, repo, tag, sha string) error {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return err
	}
	payload := map[string]string{"ref": "refs/tags/" + tag, "sha": sha}
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/git/refs", owner, name), payload, nil)
}

// DeleteTagRef 删除 tag；不存在时视为成功。
func (c *Client) DeleteTagRef(ctx context.Context, repo, tag string) error {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return err
	}
	err = c.do(ctx, http.MethodDelete, fmt.Sprintf("/repos/%s/%s/git/refs/tags/%s", owner, name, url.PathEscape(tag)), nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// CreateRelease 创建 GitHub Release（tag 必须已存在）。
func (c *Client) CreateRelease(ctx context.Context, repo, tag, target, title, body string) (*Release, error) {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"tag_name": tag, "name": title, "body": body,
		"draft": false, "prerelease": false, "make_latest": "true",
	}
	if target != "" {
		payload["target_commitish"] = target
	}
	var out Release
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/releases", owner, name), payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteRelease 删除 GitHub Release；不存在时视为成功。
func (c *Client) DeleteRelease(ctx context.Context, repo string, id int64) error {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return err
	}
	err = c.do(ctx, http.MethodDelete, fmt.Sprintf("/repos/%s/%s/releases/%d", owner, name, id), nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// RemoveLabel 移除 Issue 上的标签；标签不存在时视为成功。
func (c *Client) RemoveLabel(ctx context.Context, repo string, number int, label string) error {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/labels/%s", owner, name, number, url.PathEscape(label))
	err = c.do(ctx, http.MethodDelete, path, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// Asset 是 Release 附件的响应子集。
type Asset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// UploadReleaseAsset 把制品作为 Release 附件上传（octet-stream）。
// 用 /repos/{owner}/{repo}/releases/{id}/assets?name= 形式，api.github.com 与 GitHub Enterprise 都支持。
func (c *Client) UploadReleaseAsset(ctx context.Context, repo string, releaseID int64, name string, data []byte) (*Asset, error) {
	owner, name0, err := SplitRepo(repo)
	if err != nil {
		return nil, err
	}
	q := url.Values{"name": {name}}
	path := fmt.Sprintf("/repos/%s/%s/releases/%d/assets?%s", owner, name0, releaseID, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/octet-stream")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: upload asset: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	var out Asset
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("github: decode asset: %w", err)
	}
	return &out, nil
}
