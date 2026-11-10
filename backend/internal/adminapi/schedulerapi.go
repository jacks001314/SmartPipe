package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kataras/iris/v12"
)

// registerScheduler 把调度器总览、worker 详情和控制动作接入管理台。
func (a *API) registerScheduler(party iris.Party) {
	party.Get("/scheduler/overview", a.schedulerOverview)
	party.Get("/scheduler/messages", a.schedulerMessages)
	party.Get("/scheduler/workers/{id}", a.schedulerWorkerDetail)
	party.Get("/scheduler/workers/{id}/output", a.schedulerWorkerOutput)
	party.Get("/scheduler/workers/{id}/output/{item}/raw", a.schedulerWorkerOutputRaw)
	party.Get("/scheduler/workers/{id}/stream", a.schedulerWorkerStream)
	party.Post("/scheduler/workers/{id}/action", a.schedulerWorkerAction)
}

type schedulerWorkerSummary struct {
	ID             string   `json:"id"`
	ProjectID      string   `json:"project_id"`
	Issue          int      `json:"issue_number"`
	State          string   `json:"state"`
	AllowedActions []string `json:"allowed_actions"`
}

type schedulerDetailEnvelope struct {
	Worker schedulerWorkerSummary `json:"worker"`
}

func (a *API) schedulerOverview(c iris.Context) {
	raw, status, err := a.schedulerDo(c.Request().Context(), http.MethodGet, "/internal/scheduler/overview", nil)
	if err != nil {
		a.failScheduler(c, status, err)
		return
	}
	_ = c.JSON(envelope{Data: json.RawMessage(raw)})
}

func (a *API) schedulerMessages(c iris.Context) {
	items, err := a.store.ListLanxinMessages(c.Request().Context(), intQuery(c, "limit", 100))
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) schedulerWorkerDetail(c iris.Context) {
	id := strings.TrimSpace(c.Params().Get("id"))
	if id == "" {
		a.badRequest(c, "worker id required")
		return
	}
	raw, status, err := a.schedulerDo(c.Request().Context(), http.MethodGet,
		"/internal/scheduler/workers/"+id, nil)
	if err != nil {
		a.failScheduler(c, status, err)
		return
	}
	_ = c.JSON(envelope{Data: json.RawMessage(raw)})
}

func (a *API) schedulerWorkerOutput(c iris.Context) {
	id := strings.TrimSpace(c.Params().Get("id"))
	if id == "" {
		a.badRequest(c, "worker id required")
		return
	}
	raw, status, err := a.schedulerDo(c.Request().Context(), http.MethodGet,
		"/internal/scheduler/workers/"+url.PathEscape(id)+"/output?"+c.Request().URL.RawQuery, nil)
	if err != nil {
		a.failScheduler(c, status, err)
		return
	}
	_ = c.JSON(envelope{Data: json.RawMessage(raw)})
}

func (a *API) schedulerWorkerOutputRaw(c iris.Context) {
	id := strings.TrimSpace(c.Params().Get("id"))
	item := strings.TrimSpace(c.Params().Get("item"))
	if id == "" || item == "" {
		a.badRequest(c, "worker id and item id required")
		return
	}
	raw, status, err := a.schedulerDo(c.Request().Context(), http.MethodGet,
		"/internal/scheduler/workers/"+url.PathEscape(id)+"/output/"+url.PathEscape(item)+"/raw?"+c.Request().URL.RawQuery, nil)
	if err != nil {
		a.failScheduler(c, status, err)
		return
	}
	_ = c.JSON(envelope{Data: json.RawMessage(raw)})
}

// schedulerWorkerStream 透明转发调度器的 SSE 帧，并在每块后 Flush。管理台
// 的认证仍由 /admin 分组中间件完成，调度器 token 只存在服务端。
func (a *API) schedulerWorkerStream(c iris.Context) {
	// 管理台全局启用了压缩；SSE 必须绕过压缩缓冲，否则 Flush 只能把压缩器
	// 的内部缓冲推出去，浏览器仍可能长时间收不到事件。
	c.CompressWriter(false)
	id := strings.TrimSpace(c.Params().Get("id"))
	if id == "" {
		a.badRequest(c, "worker id required")
		return
	}
	if strings.TrimSpace(a.schedulerURL) == "" {
		a.failScheduler(c, http.StatusServiceUnavailable, fmt.Errorf("未配置 SMARTPIPE_SCHEDULER_URL"))
		return
	}
	target := strings.TrimRight(a.schedulerURL, "/") + "/internal/scheduler/workers/" + url.PathEscape(id) +
		"/stream?" + c.Request().URL.RawQuery
	req, err := http.NewRequestWithContext(c.Request().Context(), http.MethodGet, target, nil)
	if err != nil {
		a.failScheduler(c, http.StatusBadGateway, err)
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	if last := strings.TrimSpace(c.GetHeader("Last-Event-ID")); last != "" {
		req.Header.Set("Last-Event-ID", last)
	}
	if a.schedulerToken != "" {
		req.Header.Set("X-Scheduler-Token", a.schedulerToken)
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		a.failScheduler(c, http.StatusBadGateway, fmt.Errorf("调度器不可达: %w", err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		a.failScheduler(c, resp.StatusCode, fmt.Errorf("%s", strings.TrimSpace(string(raw))))
		return
	}
	flusher, ok := c.ResponseWriter().Flusher()
	if !ok {
		a.failScheduler(c, http.StatusHTTPVersionNotSupported, fmt.Errorf("当前代理不支持流式响应"))
		return
	}
	c.ContentType("text/event-stream")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	buf := make([]byte, 32<<10)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := c.ResponseWriter().Write(buf[:n]); writeErr != nil {
				return
			}
			flusher.Flush()
		}
		if readErr != nil {
			return
		}
	}
}

func (a *API) schedulerWorkerAction(c iris.Context) {
	id := strings.TrimSpace(c.Params().Get("id"))
	if id == "" {
		a.badRequest(c, "worker id required")
		return
	}
	var in struct {
		Action string `json:"action"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	action := strings.ToLower(strings.TrimSpace(in.Action))
	switch action {
	case "pause", "resume", "retry", "stop":
	default:
		a.badRequest(c, "action 必须是 pause/resume/retry/stop")
		return
	}

	ctx := c.Request().Context()
	var detail schedulerDetailEnvelope
	if raw, status, err := a.schedulerDo(ctx, http.MethodGet, "/internal/scheduler/workers/"+id, nil); err != nil {
		a.failScheduler(c, status, err)
		return
	} else if err := json.Unmarshal(raw, &detail); err != nil {
		a.failScheduler(c, http.StatusBadGateway, fmt.Errorf("调度器返回了无法解析的 worker 详情: %w", err))
		return
	}

	body, _ := json.Marshal(map[string]string{"action": action})
	raw, status, err := a.schedulerDo(ctx, http.MethodPost,
		"/internal/scheduler/workers/"+id+"/action", body)
	if err != nil {
		a.audit(ctx, c, "scheduler.worker."+action, "scheduler_worker", id, projectPtr(detail.Worker.ProjectID), "failed",
			map[string]any{"issue": detail.Worker.Issue, "from": detail.Worker.State, "action": action})
		a.failScheduler(c, status, err)
		return
	}
	a.audit(ctx, c, "scheduler.worker."+action, "scheduler_worker", id, projectPtr(detail.Worker.ProjectID), "ok",
		map[string]any{"issue": detail.Worker.Issue, "from": detail.Worker.State, "action": action})
	_ = c.JSON(envelope{Data: json.RawMessage(raw)})
}

func projectPtr(id string) *string {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	return &id
}

func (a *API) schedulerDo(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	if strings.TrimSpace(a.schedulerURL) == "" {
		return nil, http.StatusServiceUnavailable, fmt.Errorf("未配置 SMARTPIPE_SCHEDULER_URL")
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.schedulerURL, "/")+path, reader)
	if err != nil {
		return nil, http.StatusBadGateway, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.schedulerToken != "" {
		req.Header.Set("X-Scheduler-Token", a.schedulerToken)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("调度器不可达: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("读取调度器响应失败: %w", err)
	}
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		var payload struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &payload) == nil && strings.TrimSpace(payload.Error) != "" {
			msg = payload.Error
		}
		if msg == "" {
			msg = fmt.Sprintf("调度器返回 HTTP %d", resp.StatusCode)
		}
		return nil, resp.StatusCode, fmt.Errorf("%s", msg)
	}
	return raw, resp.StatusCode, nil
}

func (a *API) failScheduler(c iris.Context, status int, err error) {
	if status <= 0 || status == http.StatusUnauthorized || status == http.StatusForbidden {
		status = http.StatusBadGateway
	}
	c.StatusCode(status)
	_ = c.JSON(envelope{Err: &apiError{Code: "scheduler_error", Message: err.Error()}})
}
