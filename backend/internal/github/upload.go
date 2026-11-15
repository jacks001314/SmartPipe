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
	"strings"
	"time"
)

// uploadTimeout 比普通 API 调用更宽：聊天截图最大 8 MB，慢链路上 30 秒不够。
const uploadTimeout = 2 * time.Minute

// UploadUserAttachment 通过 GitHub 的 user-attachments 接口上传图片或视频，
// 返回可直接写进 Issue 正文/评论的 URL。
//
// 该接口要求账号身份（PAT/OAuth），GitHub App 安装令牌会返回 404；调用方应
// 先用 bridge.UserClientForRepo 选好凭据。GitHub Enterprise Server 不支持此接口。
func (c *Client) UploadUserAttachment(ctx context.Context, repo, name, contentType string, data []byte) (string, error) {
	owner, repoName, err := SplitRepo(repo)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", errors.New("github: attachment is empty")
	}
	info, _, err := c.gh.Repositories.Get(ctx, owner, repoName)
	if err != nil {
		return "", wrapErr(err)
	}
	repoID := info.GetID()
	if repoID <= 0 {
		return "", errors.New("github: repository id missing for attachment upload")
	}

	base := uploadURLFor(c.BaseURL)
	if base == nil {
		return "", fmt.Errorf("github: invalid api base %q", c.BaseURL)
	}
	target := *base
	target.Path = strings.TrimRight(base.Path, "/") + "/user-attachments/assets"
	query := target.Query()
	query.Set("name", strings.TrimSpace(name))
	query.Set("content_type", strings.TrimSpace(contentType))
	query.Set("repository_id", strconv.FormatInt(repoID, 10))
	target.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.ContentLength = int64(len(data))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Accept", "application/vnd.github+json")
	// GitHub 要求所有 API 请求带 User-Agent，缺少会被拒绝。
	req.Header.Set("User-Agent", "smartpipe")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = httpClientForUpload()
	} else if httpClient.Timeout < uploadTimeout {
		clone := *httpClient
		clone.Timeout = uploadTimeout
		httpClient = &clone
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body := strings.TrimSpace(string(raw))
		if body == "" {
			body = http.StatusText(resp.StatusCode)
		}
		return "", &APIError{Status: resp.StatusCode, Body: body}
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("github: decode attachment response: %w", err)
	}
	if strings.TrimSpace(out.URL) == "" {
		return "", errors.New("github: attachment upload returned no url")
	}
	parsed, err := url.Parse(out.URL)
	if err != nil {
		return "", fmt.Errorf("github: attachment upload returned invalid url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("github: attachment upload returned non-absolute url %q", out.URL)
	}
	return out.URL, nil
}

func httpClientForUpload() *http.Client {
	return &http.Client{Timeout: uploadTimeout}
}
