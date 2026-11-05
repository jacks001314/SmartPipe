// Package github 是 SmartPipe 需要的最小 GitHub REST 客户端：
// 建 Issue、写评论、加标签、读 Issue。只用标准库，便于在测试里替换 BaseURL。
package github

import (
	"bytes"
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
	"strings"
	"time"
	"net/url"
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
}

// New 构造客户端；token 可以是 PAT 或 installation token。
func New(baseURL, token string) *Client {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = DefaultAPIBase
	}
	return &Client{BaseURL: base, Token: strings.TrimSpace(token), HTTP: &http.Client{Timeout: 30 * time.Second}}
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

func (c *Client) do(ctx context.Context, method, path string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("github: encode body: %w", err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &APIError{Status: resp.StatusCode, Body: string(data)}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{Status: resp.StatusCode, Body: string(data)}
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("github: decode response: %w", err)
		}
	}
	return nil
}

// CreateIssue 在 owner/repo 建 Issue。
func (c *Client) CreateIssue(ctx context.Context, repo, title, body string, labels []string) (*Issue, error) {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"title": title, "body": body}
	if len(labels) > 0 {
		payload["labels"] = labels
	}
	var out Issue
	if err := c.do(ctx, http.MethodPost, "/repos/"+owner+"/"+name+"/issues", payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Comment 在指定 Issue 下写评论。
func (c *Client) Comment(ctx context.Context, repo string, number int, body string) error {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, name, number),
		map[string]string{"body": body}, nil)
}

// AddLabels 给 Issue 追加标签。
func (c *Client) AddLabels(ctx context.Context, repo string, number int, labels []string) error {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return err
	}
	add := func() error {
		return c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/issues/%d/labels", owner, name, number),
			map[string]any{"labels": labels}, nil)
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
	var existing struct {
		Name string `json:"name"`
	}
	getErr := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/labels/%s", owner, name, url.PathEscape(label)), nil, &existing)
	if getErr == nil {
		return nil
	}
	var apiErr *APIError
	if !errors.As(getErr, &apiErr) || apiErr.Status != http.StatusNotFound {
		return getErr
	}
	createErr := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/labels", owner, name),
		map[string]any{"name": label, "color": "ededed"}, nil)
	if createErr == nil {
		return nil
	}
	if errors.As(createErr, &apiErr) && apiErr.Status == http.StatusUnprocessableEntity {
		return nil // 并发创建时可能已经存在
	}
	return createErr
}

// GetIssue 读取 Issue。
func (c *Client) GetIssue(ctx context.Context, repo string, number int) (*Issue, error) {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return nil, err
	}
	var out Issue
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/issues/%d", owner, name, number), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
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
	resp, err := http.DefaultClient.Do(req)
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
