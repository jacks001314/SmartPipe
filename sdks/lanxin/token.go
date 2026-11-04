package lanxin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type tokenManager struct {
	c *Client

	mu        sync.Mutex
	token     string
	expiresAt time.Time
	now       func() time.Time // 测试注入
}

func newTokenManager(c *Client) *tokenManager {
	return &tokenManager{c: c, now: time.Now}
}

type tokenData struct {
	AppToken  string `json:"appToken"`
	ExpiresIn int    `json:"expiresIn"`
}

// refreshMargin 提前刷新窗口（官方建议 300s）。
const refreshMargin = 300 * time.Second

func (t *tokenManager) get(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && t.now().Before(t.expiresAt.Add(-refreshMargin)) {
		return t.token, nil
	}
	return t.refresh(ctx)
}

func (t *tokenManager) refresh(ctx context.Context) (string, error) {
	q := url.Values{}
	q.Set("grant_type", "client_credential")
	q.Set("appid", t.c.appID)
	q.Set("secret", t.c.secret)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.c.url("/v1/apptoken/create", q.Encode()), nil)
	if err != nil {
		return "", err
	}
	resp, err := t.c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("lanxin: apptoken/create: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("lanxin: apptoken/create: decode (http %d): %w", resp.StatusCode, err)
	}
	if env.ErrCode != 0 {
		return "", &APIError{Code: env.ErrCode, Msg: env.ErrMsg, Path: "/v1/apptoken/create"}
	}
	var td tokenData
	if err := json.Unmarshal(env.Data, &td); err != nil {
		return "", fmt.Errorf("lanxin: apptoken/create: decode data: %w", err)
	}
	if td.AppToken == "" {
		return "", fmt.Errorf("lanxin: apptoken/create: empty appToken")
	}
	t.token = td.AppToken
	if td.ExpiresIn <= 0 {
		td.ExpiresIn = 7200
	}
	t.expiresAt = t.now().Add(time.Duration(td.ExpiresIn) * time.Second)
	return t.token, nil
}
