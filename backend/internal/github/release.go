package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	gh "github.com/google/go-github/v68/github"
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
	opts := &gh.IssueListByRepoOptions{
		State:       "open",
		ListOptions: gh.ListOptions{PerPage: limit},
	}
	if label != "" {
		opts.Labels = []string{label}
	}
	items, _, err := c.gh.Issues.ListByRepo(ctx, owner, name, opts)
	if err != nil {
		return nil, wrapErr(err)
	}
	issues := make([]Issue, 0, len(items))
	for _, item := range items {
		if item == nil || item.PullRequestLinks != nil {
			continue
		}
		issues = append(issues, *toIssue(item))
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
	commit, _, err := c.gh.Repositories.GetCommit(ctx, owner, name, ref, nil)
	if err != nil {
		return "", wrapErr(err)
	}
	sha := commit.GetSHA()
	if sha == "" {
		return "", fmt.Errorf("github: branch %s has no commit", ref)
	}
	return sha, nil
}

// TagSHA 读取已存在 tag 指向的提交；tag 不存在时返回空串与 nil。
func (c *Client) TagSHA(ctx context.Context, repo, tag string) (string, error) {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return "", err
	}
	ref, _, err := c.gh.Git.GetRef(ctx, owner, name, "tags/"+tag)
	if err != nil {
		if IsNotFound(wrapErr(err)) {
			return "", nil
		}
		return "", wrapErr(err)
	}
	return ref.GetObject().GetSHA(), nil
}

// CreateTagRef 创建轻量 tag。
func (c *Client) CreateTagRef(ctx context.Context, repo, tag, sha string) error {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return err
	}
	_, _, err = c.gh.Git.CreateRef(ctx, owner, name, &gh.Reference{
		Ref:    gh.String("refs/tags/" + tag),
		Object: &gh.GitObject{SHA: gh.String(sha)},
	})
	return wrapErr(err)
}

// DeleteTagRef 删除 tag；不存在时视为成功。
func (c *Client) DeleteTagRef(ctx context.Context, repo, tag string) error {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return err
	}
	_, err = c.gh.Git.DeleteRef(ctx, owner, name, "tags/"+tag)
	if IsNotFound(wrapErr(err)) {
		return nil
	}
	return wrapErr(err)
}

func toRelease(in *gh.RepositoryRelease) *Release {
	if in == nil {
		return nil
	}
	out := &Release{
		ID:         in.GetID(),
		HTMLURL:    in.GetHTMLURL(),
		TagName:    in.GetTagName(),
		Name:       in.GetName(),
		Draft:      in.GetDraft(),
		Prerelease: in.GetPrerelease(),
	}
	if in.CreatedAt != nil {
		out.CreatedAt = in.CreatedAt.Time
	}
	return out
}

// CreateRelease 创建 GitHub Release（tag 必须已存在）。
func (c *Client) CreateRelease(ctx context.Context, repo, tag, target, title, body string) (*Release, error) {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return nil, err
	}
	req := &gh.RepositoryRelease{
		TagName:    gh.String(tag),
		Name:       gh.String(title),
		Body:       gh.String(body),
		Draft:      gh.Bool(false),
		Prerelease: gh.Bool(false),
		MakeLatest: gh.String("true"),
	}
	if target != "" {
		req.TargetCommitish = gh.String(target)
	}
	rel, _, err := c.gh.Repositories.CreateRelease(ctx, owner, name, req)
	if err != nil {
		return nil, wrapErr(err)
	}
	return toRelease(rel), nil
}

// DeleteRelease 删除 GitHub Release；不存在时视为成功。
func (c *Client) DeleteRelease(ctx context.Context, repo string, id int64) error {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return err
	}
	_, err = c.gh.Repositories.DeleteRelease(ctx, owner, name, id)
	if IsNotFound(wrapErr(err)) {
		return nil
	}
	return wrapErr(err)
}

// RemoveLabel 移除 Issue 上的标签；标签不存在时视为成功。
func (c *Client) RemoveLabel(ctx context.Context, repo string, number int, label string) error {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return err
	}
	_, err = c.gh.Issues.RemoveLabelForIssue(ctx, owner, name, number, label)
	if IsNotFound(wrapErr(err)) {
		return nil
	}
	return wrapErr(err)
}

// Asset 是 Release 附件的响应子集。
type Asset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// UploadReleaseAsset 把制品作为 Release 附件上传（octet-stream）。
func (c *Client) UploadReleaseAsset(ctx context.Context, repo string, releaseID int64, name string, data []byte) (*Asset, error) {
	owner, repoName, err := SplitRepo(repo)
	if err != nil {
		return nil, err
	}
	relative := fmt.Sprintf("repos/%s/%s/releases/%d/assets?name=%s", owner, repoName, releaseID, url.QueryEscape(name))
	req, err := c.gh.NewUploadRequest(relative, bytes.NewReader(data), int64(len(data)), "application/octet-stream")
	if err != nil {
		return nil, err
	}
	var out gh.ReleaseAsset
	resp, err := c.gh.Do(ctx, req, &out)
	if err != nil {
		return nil, wrapErr(err)
	}
	if resp == nil || resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &APIError{Status: http.StatusInternalServerError, Body: "github: upload asset failed"}
	}
	return &Asset{
		ID:                 out.GetID(),
		Name:               out.GetName(),
		Size:               int64(out.GetSize()),
		BrowserDownloadURL: out.GetBrowserDownloadURL(),
	}, nil
}
