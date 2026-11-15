package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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

// TagInfo 是仓库标签的最小视图。
type TagInfo struct {
	Name string `json:"name"`
	SHA  string `json:"sha"`
}

// CommitInfo 是发布预览需要的提交信息。
type CommitInfo struct {
	SHA     string    `json:"sha"`
	Date    time.Time `json:"date"`
	Message string    `json:"message"`
}

// SourceArchive 是一次源码归档下载结果，调用方负责关闭 Body。
type SourceArchive struct {
	Body        io.ReadCloser
	Size        int64
	ContentType string
}

// DownloadArchive 下载仓库指定 ref 的 tar.gz 源码归档。
// 它走 /repos/{owner}/{repo}/tarball/{ref}，私有仓库需要 token。
func (c *Client) DownloadArchive(ctx context.Context, repo, ref string) (*SourceArchive, error) {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return nil, err
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("github: archive ref is required")
	}
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = DefaultAPIBase
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/tarball/%s", base, url.PathEscape(owner), url.PathEscape(name), url.PathEscape(ref))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token := strings.TrimSpace(c.Token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := c.HTTP
	if client == nil {
		client = httpClient
	}
	// 复用同一个 Transport（代理设置在它上面），但源码包可能较大，放宽整体超时。
	download := *client
	if download.Timeout < 5*time.Minute {
		download.Timeout = 5 * time.Minute
	}
	resp, err := download.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, &APIError{Status: resp.StatusCode, Body: string(body)}
	}
	return &SourceArchive{Body: resp.Body, Size: resp.ContentLength, ContentType: resp.Header.Get("Content-Type")}, nil
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

// ListTags 返回仓库标签，按 GitHub 的默认顺序（新标签在前）。
func (c *Client) ListTags(ctx context.Context, repo string, limit int) ([]TagInfo, error) {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	items, _, err := c.gh.Repositories.ListTags(ctx, owner, name, &gh.ListOptions{PerPage: limit})
	if err != nil {
		return nil, wrapErr(err)
	}
	out := make([]TagInfo, 0, len(items))
	for _, item := range items {
		if item == nil || item.GetName() == "" {
			continue
		}
		sha := item.GetCommit().GetSHA()
		if sha == "" {
			continue
		}
		out = append(out, TagInfo{Name: item.GetName(), SHA: sha})
	}
	return out, nil
}

// GetCommitInfo 读取提交的 SHA、提交日期和说明。
func (c *Client) GetCommitInfo(ctx context.Context, repo, ref string) (*CommitInfo, error) {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return nil, err
	}
	item, _, err := c.gh.Repositories.GetCommit(ctx, owner, name, ref, nil)
	if err != nil {
		return nil, wrapErr(err)
	}
	out := &CommitInfo{SHA: item.GetSHA()}
	if item.GetCommit() != nil {
		out.Message = item.GetCommit().GetMessage()
		if committer := item.GetCommit().GetCommitter(); committer != nil {
			out.Date = committer.GetDate().Time
		}
		if out.Date.IsZero() {
			if author := item.GetCommit().GetAuthor(); author != nil {
				out.Date = author.GetDate().Time
			}
		}
	}
	return out, nil
}

// ReadFile 读取仓库中某个 ref 上的文本文件；文件不存在时返回空串和 false。
func (c *Client) ReadFile(ctx context.Context, repo, ref, path string) (string, bool, error) {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return "", false, err
	}
	file, _, resp, err := c.gh.Repositories.GetContents(ctx, owner, name, path, &gh.RepositoryContentGetOptions{Ref: ref})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return "", false, nil
		}
		return "", false, wrapErr(err)
	}
	if file == nil || file.Content == nil {
		return "", false, nil
	}
	content, err := file.GetContent()
	if err != nil {
		return "", false, wrapErr(err)
	}
	return content, true, nil
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
