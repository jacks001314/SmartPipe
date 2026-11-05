package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"smartpipe/backend/internal/mcp"
)

// mcpRecorder 把工具调用记录异步回写到管理接口，避免拖慢 Agent 的调用。
// 队列满时丢弃并计数，审计失败不影响工具执行。
type mcpRecorder struct {
	adminURL   string
	adminToken string
	client     *http.Client
	queue      chan mcp.CallRecord
	dropped    chan struct{}
}

func newMCPRecorder(adminURL, adminToken string, size int) *mcpRecorder {
	if size <= 0 {
		size = 256
	}
	r := &mcpRecorder{
		adminURL:   strings.TrimRight(adminURL, "/"),
		adminToken: adminToken,
		client:     &http.Client{Timeout: 5 * time.Second},
		queue:      make(chan mcp.CallRecord, size),
		dropped:    make(chan struct{}, 1),
	}
	go r.loop()
	return r
}

func (r *mcpRecorder) Record(ctx context.Context, rec mcp.CallRecord) {
	select {
	case r.queue <- rec:
	default:
		select {
		case r.dropped <- struct{}{}:
			log.Printf("tool-call 审计队列已满，丢弃记录（tool=%s status=%s）", rec.Tool, rec.Status)
		default:
		}
	}
}

func (r *mcpRecorder) loop() {
	for rec := range r.queue {
		if err := r.post(rec); err != nil {
			log.Printf("tool-call 审计写入失败: %v", err)
		}
	}
}

func (r *mcpRecorder) post(rec mcp.CallRecord) error {
	summary, err := json.Marshal(rec.ArgsSummary)
	if err != nil {
		summary = []byte("{}")
	}
	payload, err := json.Marshal(map[string]any{
		"project_id":   rec.ProjectID,
		"token_id":     rec.TokenID,
		"agent_name":   rec.AgentName,
		"role":         rec.Role,
		"tool":         rec.Tool,
		"status":       rec.Status,
		"duration_ms":  rec.DurationMS,
		"args_summary": json.RawMessage(summary),
		"error":        rec.Error,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.adminURL+"/admin/tool-calls", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.adminToken != "" {
		req.Header.Set("Authorization", "Bearer "+r.adminToken)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return &httpError{status: resp.StatusCode, body: strings.TrimSpace(string(raw))}
	}
	return nil
}

type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string {
	return "HTTP " + strconv.Itoa(e.status) + ": " + e.body
}
