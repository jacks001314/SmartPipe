package lanxin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Envelope 蓝信 API 统一响应格式。
type Envelope struct {
	ErrCode int             `json:"errCode"`
	ErrMsg  string          `json:"errMsg"`
	Data    json.RawMessage `json:"data"`
}

// APIError 表示 errCode != 0 的业务错误。
type APIError struct {
	Code int
	Msg  string
	Path string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("lanxin: %s: errCode=%d errMsg=%q", e.Path, e.Code, e.Msg)
}

// call 发起一次带 appToken 的 API 调用并校验 errCode。
// skipToken 的接口（apptoken/create、ws/endpoint/create）不走该逻辑，见各自实现。
func (c *Client) call(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	tok, err := c.tok.get(ctx)
	if err != nil {
		return err
	}
	if query == nil {
		query = url.Values{}
	}
	query.Set("app_token", tok)

	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("lanxin: marshal body: %w", err)
		}
		rd = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.url(path, query.Encode()), rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("lanxin: %s: %w", path, err)
	}
	defer resp.Body.Close()

	var env Envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("lanxin: %s: decode response (http %d): %w", path, resp.StatusCode, err)
	}
	if env.ErrCode != 0 {
		return &APIError{Code: env.ErrCode, Msg: env.ErrMsg, Path: path}
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("lanxin: %s: decode data: %w", path, err)
		}
	}
	return nil
}
