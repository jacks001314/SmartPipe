package lanxin

import (
	"net/http"
	"strings"
	"time"
)

// DefaultAPIBase 蓝信公有云 API 网关。企业私有部署请用 WithAPIBase 覆盖。
const DefaultAPIBase = "https://open.e.lanxin.cn/open/apigw"

// Client 是蓝信个人机器人 API 客户端，goroutine 安全。
type Client struct {
	apiBase string
	appID   string
	secret  string
	http    *http.Client

	tok *tokenManager
}

// Option 配置 Client。
type Option func(*Client)

// WithAPIBase 覆盖 API 网关地址（企业私有部署必填）。
func WithAPIBase(base string) Option {
	return func(c *Client) { c.apiBase = strings.TrimRight(base, "/") }
}

// WithHTTPClient 覆盖底层 HTTP 客户端（超时/代理/审计）。
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// New 创建客户端。
func New(appID, secret string, opts ...Option) *Client {
	c := &Client{
		apiBase: DefaultAPIBase,
		appID:   appID,
		secret:  secret,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	c.tok = newTokenManager(c)
	return c
}

func (c *Client) url(path string, query string) string {
	u := c.apiBase + path
	if query != "" {
		u += "?" + query
	}
	return u
}
