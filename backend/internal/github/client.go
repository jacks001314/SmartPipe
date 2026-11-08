// Package github 是 SmartPipe 用到的 GitHub REST 能力，内部基于 google/go-github，
// 对外保留最小接口：建 Issue、评论、标签、读取 Issue，以及 App 安装令牌。
package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	gh "github.com/google/go-github/v68/github"
)

// DefaultAPIBase 是 GitHub.com 的 REST 入口。
const DefaultAPIBase = "https://api.github.com"

// ErrUnauthorized 表示 token 无效。
var ErrUnauthorized = errors.New("github: unauthorized")

// APIError 包装非 2xx 响应。
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 300 {
		body = body[:300] + "..."
	}
	return fmt.Sprintf("github: HTTP %d: %s", e.Status, body)
}

// Client 是 GitHub REST 客户端。
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client

	gh *gh.Client
}

// httpClient 是包内所有 GitHub HTTP 请求共用的客户端，部署方可以注入带代理的版本。
var httpClient = &http.Client{Timeout: 30 * time.Second}

// SetHTTPClient 替换包内默认客户端（例如注入代理）。
func SetHTTPClient(client *http.Client) {
	if client != nil {
		httpClient = client
	}
}

// HTTPClient 返回当前包内使用的客户端。
func HTTPClient() *http.Client { return httpClient }

// New 构造客户端；token 可以是 PAT 或 installation token。
func New(baseURL, token string) *Client {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = DefaultAPIBase
	}
	hc := httpClient
	client := gh.NewClient(hc)
	if token = strings.TrimSpace(token); token != "" {
		client = client.WithAuthToken(token)
	}
	if base != DefaultAPIBase {
		if u, err := url.Parse(base + "/"); err == nil {
			client.BaseURL = u
			client.UploadURL = uploadURLFor(base)
		}
	}
	return &Client{BaseURL: base, Token: token, HTTP: hc, gh: client}
}

// uploadURLFor 返回 Release 附件上传基地址：GitHub.com 用 uploads.github.com，
// 企业版把 api.<host> 换成 uploads.<host>；本地假服务保持原地址。
func uploadURLFor(base string) *url.URL {
	u, err := url.Parse(strings.TrimRight(base, "/") + "/")
	if err != nil {
		return nil
	}
	switch {
	case u.Host == "api.github.com":
		u.Host = "uploads.github.com"
	case strings.HasPrefix(u.Host, "api."):
		u.Host = "uploads." + strings.TrimPrefix(u.Host, "api.")
	}
	return u
}

// wrapErr 把 go-github 的 ErrorResponse 映射成项目内的 APIError，保持 IsNotFound 等判断不变。
func wrapErr(err error) error {
	if err == nil {
		return nil
	}
	var er *gh.ErrorResponse
	if errors.As(err, &er) && er.Response != nil {
		body := strings.TrimSpace(er.Message)
		if body == "" {
			body = err.Error()
		}
		return &APIError{Status: er.Response.StatusCode, Body: body}
	}
	return err
}

func toIssue(in *gh.Issue) *Issue {
	if in == nil {
		return nil
	}
	out := &Issue{
		Number:  in.GetNumber(),
		HTMLURL: in.GetHTMLURL(),
		Title:   in.GetTitle(),
		Body:    in.GetBody(),
		State:   in.GetState(),
	}
	for _, l := range in.Labels {
		out.Labels = append(out.Labels, Label{Name: l.GetName()})
	}
	if u := in.GetUser(); u != nil {
		out.User = &User{Login: u.GetLogin()}
	}
	if in.CreatedAt != nil {
		out.CreatedAt = in.CreatedAt.Time
	}
	if in.UpdatedAt != nil {
		out.UpdatedAt = in.UpdatedAt.Time
	}
	if in.PullRequestLinks != nil {
		out.PullRequest = json.RawMessage(`{"url":""}`)
	}
	return out
}

// Label 是 GitHub 标签。
type Label struct {
	Name string `json:"name"`
}

// User 是 GitHub 用户。
type User struct {
	Login string `json:"login"`
}

// Issue 是 GitHub Issue 的响应子集。
type Issue struct {
	Number      int             `json:"number"`
	HTMLURL     string          `json:"html_url"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	State       string          `json:"state"`
	Labels      []Label         `json:"labels"`
	User        *User           `json:"user"`
	PullRequest json.RawMessage `json:"pull_request"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// CreateIssue 在 owner/repo 建 Issue。
func (c *Client) CreateIssue(ctx context.Context, repo, title, body string, labels []string) (*Issue, error) {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return nil, err
	}
	req := &gh.IssueRequest{Title: gh.String(title), Body: gh.String(body)}
	if len(labels) > 0 {
		req.Labels = &labels
	}
	issue, _, err := c.gh.Issues.Create(ctx, owner, name, req)
	if err != nil {
		return nil, wrapErr(err)
	}
	return toIssue(issue), nil
}

// Comment 在指定 Issue 下写评论。
func (c *Client) Comment(ctx context.Context, repo string, number int, body string) error {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return err
	}
	_, _, err = c.gh.Issues.CreateComment(ctx, owner, name, number, &gh.IssueComment{Body: gh.String(body)})
	return wrapErr(err)
}

// AddLabels 给 Issue 追加标签。
func (c *Client) AddLabels(ctx context.Context, repo string, number int, labels []string) error {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return err
	}
	add := func() error {
		_, _, err := c.gh.Issues.AddLabelsToIssue(ctx, owner, name, number, labels)
		return wrapErr(err)
	}
	err = add()
	if err == nil {
		return nil
	}
	// 真实 GitHub 对"给已有 Issue 打不存在的标签"返回 422；先补建标签再重试一次。
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnprocessableEntity {
		return err
	}
	for _, label := range labels {
		if err := c.EnsureLabel(ctx, repo, label); err != nil {
			return err
		}
	}
	return add()
}

// EnsureLabel 保证仓库里存在该标签：已存在直接返回，不存在则创建。
func (c *Client) EnsureLabel(ctx context.Context, repo, label string) error {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return err
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return nil
	}
	if _, _, err := c.gh.Issues.GetLabel(ctx, owner, name, label); err == nil {
		return nil
	} else if !IsNotFound(wrapErr(err)) {
		return wrapErr(err)
	}
	_, _, err = c.gh.Issues.CreateLabel(ctx, owner, name, &gh.Label{Name: gh.String(label), Color: gh.String("ededed")})
	if err == nil {
		return nil
	}
	var apiErr *APIError
	if errors.As(wrapErr(err), &apiErr) && apiErr.Status == http.StatusUnprocessableEntity {
		return nil // 并发创建时可能已经存在
	}
	return wrapErr(err)
}

// GetIssue 读取 Issue。
func (c *Client) GetIssue(ctx context.Context, repo string, number int) (*Issue, error) {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return nil, err
	}
	issue, _, err := c.gh.Issues.Get(ctx, owner, name, number)
	if err != nil {
		return nil, wrapErr(err)
	}
	return toIssue(issue), nil
}

// SplitRepo 把 "owner/repo" 拆开，也接受完整 URL。
func SplitRepo(repo string) (string, string, error) {
	trimmed := strings.TrimSpace(repo)
	trimmed = strings.TrimSuffix(trimmed, ".git")
	if strings.HasPrefix(trimmed, "git@") {
		if _, after, ok := strings.Cut(trimmed, ":"); ok {
			trimmed = after
		}
	}
	if strings.Contains(trimmed, "://") {
		parts := strings.Split(strings.TrimSuffix(trimmed, "/"), "/")
		if len(parts) >= 2 {
			trimmed = parts[len(parts)-2] + "/" + parts[len(parts)-1]
		}
	}
	owner, name, ok := strings.Cut(trimmed, "/")
	if !ok || owner == "" || name == "" {
		return "", "", fmt.Errorf("github: invalid repo %q (want owner/name)", repo)
	}
	return owner, name, nil
}

// InstallationToken 用 GitHub App 私钥换取 installation token。
func InstallationToken(ctx context.Context, apiBase, appID, installationID, privateKeyPEM string, now time.Time) (string, time.Time, error) {
	jwt, err := SignAppJWT(appID, privateKeyPEM, now)
	if err != nil {
		return "", time.Time{}, err
	}
	base := strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if base == "" {
		base = DefaultAPIBase
	}
	body := strings.NewReader("{}")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/app/installations/%s/access_tokens", base, installationID), body)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", time.Time{}, &APIError{Status: resp.StatusCode, Body: string(data)}
	}
	var out struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", time.Time{}, err
	}
	expires, _ := time.Parse(time.RFC3339, out.ExpiresAt)
	return out.Token, expires, nil
}

// SignAppJWT 用应用私钥签 RS256 JWT（GitHub App 认证）。
func SignAppJWT(appID, privateKeyPEM string, now time.Time) (string, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return "", errors.New("github: no PEM block in private key")
	}
	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key = k
	} else if parsed, err2 := x509.ParsePKCS8PrivateKey(block.Bytes); err2 == nil {
		rk, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return "", errors.New("github: private key is not RSA")
		}
		key = rk
	} else {
		return "", errors.New("github: unsupported private key format")
	}
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": appID,
	}
	h, _ := json.Marshal(header)
	p, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(h) + "." + enc.EncodeToString(p)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + enc.EncodeToString(sig), nil
}
