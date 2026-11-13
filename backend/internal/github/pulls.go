package github

import (
	"context"
	"fmt"
	"strings"
	"time"

	gh "github.com/google/go-github/v68/github"
)

// IssueComment 是 Issue/PR 下的一条评论。
type IssueComment struct {
	ID        int64     `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// PullRequest 是 PR 的最小视图。
type PullRequest struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Merged  bool   `json:"merged"`
	Draft   bool   `json:"draft"`
}

// ListComments 拉取 Issue 的全部评论（最多 100 条，按创建时间升序）。
func (c *Client) ListComments(ctx context.Context, repo string, number int) ([]IssueComment, error) {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return nil, err
	}
	var out []IssueComment
	opts := &gh.IssueListCommentsOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	for page := 1; page <= 5; page++ {
		opts.Page = page
		items, _, err := c.gh.Issues.ListComments(ctx, owner, name, number, opts)
		if err != nil {
			return nil, wrapErr(err)
		}
		for _, it := range items {
			login := ""
			if it.User != nil {
				login = it.User.GetLogin()
			}
			out = append(out, IssueComment{
				ID: it.GetID(), Author: login, Body: it.GetBody(), CreatedAt: it.GetCreatedAt().Time,
			})
		}
		if len(items) < 100 {
			break
		}
	}
	return out, nil
}

// SearchIssues 在单个仓库内按标题搜索未关闭的 Issue。
func (c *Client) SearchIssues(ctx context.Context, repo, query string, limit int) ([]Issue, error) {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	q := fmt.Sprintf("repo:%s/%s is:issue is:open %s", owner, name, strings.TrimSpace(query))
	res, _, err := c.gh.Search.Issues(ctx, q, &gh.SearchOptions{ListOptions: gh.ListOptions{PerPage: limit}})
	if err != nil {
		return nil, wrapErr(err)
	}
	out := make([]Issue, 0, len(res.Issues))
	for _, it := range res.Issues {
		if it.PullRequestLinks != nil {
			continue
		}
		issue := toIssue(it)
		if issue != nil {
			out = append(out, *issue)
		}
	}
	return out, nil
}

// CreatePullRequest 创建一个 PR（可指定 draft）。
func (c *Client) CreatePullRequest(ctx context.Context, repo, head, base, title, body string, draft bool) (*PullRequest, error) {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return nil, err
	}
	pr, _, err := c.gh.PullRequests.Create(ctx, owner, name, &gh.NewPullRequest{
		Title: gh.String(title),
		Head:  gh.String(head),
		Base:  gh.String(base),
		Body:  gh.String(body),
		Draft: gh.Bool(draft),
	})
	if err != nil {
		return nil, wrapErr(err)
	}
	return &PullRequest{
		Number:  pr.GetNumber(),
		HTMLURL: pr.GetHTMLURL(),
		State:   pr.GetState(),
		Merged:  pr.GetMerged(),
		Draft:   pr.GetDraft(),
	}, nil
}

// GetPullRequest 读取 PR 状态，用于判断是否已合并。
func (c *Client) GetPullRequest(ctx context.Context, repo string, number int) (*PullRequest, error) {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return nil, err
	}
	pr, _, err := c.gh.PullRequests.Get(ctx, owner, name, number)
	if err != nil {
		return nil, wrapErr(err)
	}
	return &PullRequest{
		Number:  pr.GetNumber(),
		HTMLURL: pr.GetHTMLURL(),
		State:   pr.GetState(),
		Merged:  pr.GetMerged(),
		Draft:   pr.GetDraft(),
	}, nil
}

// MergePullRequest 把 PR squash 合入目标分支；分支保护或冲突会让调用返回错误。
func (c *Client) MergePullRequest(ctx context.Context, repo string, number int, title string) error {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return err
	}
	message := fmt.Sprintf("%s (#%d)", strings.TrimSpace(title), number)
	if _, _, err := c.gh.PullRequests.Merge(ctx, owner, name, number, message, &gh.PullRequestOptions{MergeMethod: "squash"}); err != nil {
		return wrapErr(err)
	}
	return nil
}
