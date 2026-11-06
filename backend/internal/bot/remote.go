package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RemoteLanxinConfig 是配置台下发的蓝信应用配置。
type RemoteLanxinConfig struct {
	AppID       string `json:"app_id"`
	AppSecret   string `json:"app_secret"`
	APIBase     string `json:"api_base"`
	RequireAtMe bool   `json:"require_at_me"`
}

// FetchRemoteConfig 读取配置台的蓝信配置；未配置时返回 (nil, nil)。
func FetchRemoteConfig(ctx context.Context, adminURL, token string) (*RemoteLanxinConfig, error) {
	adminURL = strings.TrimRight(strings.TrimSpace(adminURL), "/")
	if adminURL == "" {
		return nil, fmt.Errorf("未配置 SMARTPIPE_ADMIN_URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, adminURL+"/internal/lanxin/config", nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("X-Internal-Token", token)
	}
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("配置台返回 %d", resp.StatusCode)
	}
	var out struct {
		Data RemoteLanxinConfig `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("配置台响应格式错误: %w", err)
	}
	return &out.Data, nil
}

// WithRemote 用配置台的值覆盖环境变量。
func (c Config) WithRemote(rc *RemoteLanxinConfig) Config {
	if rc.AppID != "" {
		c.AppID = rc.AppID
	}
	if rc.AppSecret != "" {
		c.AppSecret = rc.AppSecret
	}
	if rc.APIBase != "" {
		c.APIBase = rc.APIBase
	}
	c.RequireAtMe = rc.RequireAtMe
	return c
}

// SameLanxin 判断连接相关配置是否发生变化。
func (c Config) SameLanxin(rc *RemoteLanxinConfig) bool {
	return c.AppID == rc.AppID && c.AppSecret == rc.AppSecret &&
		c.APIBase == rc.APIBase && c.RequireAtMe == rc.RequireAtMe
}
