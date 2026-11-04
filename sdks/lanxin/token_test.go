package lanxin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// 验证 token 缓存：窗口内不重复请求；临近过期（margin 300s）重新请求。
func TestTokenCacheAndRefresh(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/apptoken/create" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("appid") != "app-1" || r.URL.Query().Get("secret") != "s" {
			t.Errorf("bad query: %v", r.URL.RawQuery)
		}
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errCode":0,"data":{"appToken":"tok-1","expiresIn":7200}}`))
	}))
	defer srv.Close()

	c := New("app-1", "s", WithAPIBase(srv.URL))
	now := time.Now()
	c.tok.now = func() time.Time { return now }

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		tok, err := c.tok.get(ctx)
		if err != nil || tok != "tok-1" {
			t.Fatalf("get %d: tok=%q err=%v", i, tok, err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 call within window, got %d", got)
	}

	// 推进到过期前 100s（进入 300s 刷新窗口）→ 必须重新获取。
	now = now.Add(7200*time.Second - 100*time.Second)
	if _, err := c.tok.get(ctx); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected refresh near expiry, got %d calls", got)
	}
}

// 验证 errCode != 0 报 APIError。
func TestAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errCode":10005,"errMsg":"no permission"}`))
	}))
	defer srv.Close()
	c := New("a", "b", WithAPIBase(srv.URL))
	c.tok.now = func() time.Time { return time.Now().Add(24 * time.Hour) } // 跳过 token
	c.tok.token = "x"
	c.tok.expiresAt = time.Now().Add(2 * time.Hour)

	_, err := c.ListGroups(context.Background(), 0, 10)
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Code != 10005 {
		t.Fatalf("want APIError 10005, got %v", err)
	}
}
