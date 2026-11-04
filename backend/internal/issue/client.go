package issue

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

// Client 是 Issue 服务的 HTTP 客户端（Orchestrator 使用）。
type Client struct {
	base string
	hc   *http.Client
}

func NewClient(base string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), hc: &http.Client{Timeout: 10 * time.Second}}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("issue-client %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("issue-client %s %s: http %d: %s", method, path, resp.StatusCode, string(b))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) Create(ctx context.Context, in CreateInput) (*Issue, error) {
	var iss Issue
	if err := c.do(ctx, http.MethodPost, "/api/issues", in, &iss); err != nil {
		return nil, err
	}
	return &iss, nil
}

func (c *Client) Search(ctx context.Context, q string, limit int) ([]Issue, error) {
	var out []Issue
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/issues?q=%s&limit=%d", urlQueryEscape(q), limit), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) Get(ctx context.Context, id string) (*Issue, error) {
	var iss Issue
	if err := c.do(ctx, http.MethodGet, "/api/issues/"+id, nil, &iss); err != nil {
		return nil, err
	}
	return &iss, nil
}

func (c *Client) AddComment(ctx context.Context, id, author, text string) error {
	return c.do(ctx, http.MethodPost, "/api/issues/"+id+"/comments",
		map[string]string{"author": author, "text": text}, nil)
}

func urlQueryEscape(s string) string {
	return strings.ReplaceAll(urlPathEscape(s), "+", "%2B")
}

func urlPathEscape(s string) string {
	// 标准 query 转义
	return escapeQuery(s)
}

func escapeQuery(s string) string {
	var sb strings.Builder
	for _, r := range []byte(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == '~':
			sb.WriteByte(r)
		case r == ' ':
			sb.WriteByte('+')
		default:
			sb.WriteString(fmt.Sprintf("%%%02X", r))
		}
	}
	return sb.String()
}
