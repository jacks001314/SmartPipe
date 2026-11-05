package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Principal 是已认证的 Agent 身份。
type Principal struct {
	TokenID   string  `json:"token_id"`
	Name      string  `json:"name"`
	Role      string  `json:"role"`
	ProjectID *string `json:"project_id"`
}

// Authenticator 把 Bearer 令牌解析为 Agent 身份。
type Authenticator func(ctx context.Context, token string) (*Principal, error)

// Executor 执行具体工具。
type Executor interface {
	Call(ctx context.Context, p *Principal, tool string, args map[string]any) (any, error)
}

// ErrUnauthorized 表示令牌无效。
var ErrUnauthorized = errors.New("mcp: unauthorized")

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type bucket struct {
	count int
	reset time.Time
}

// Server 实现 MCP 的 initialize / tools/list / tools/call。
type Server struct {
	Auth    Authenticator
	Exec    Executor
	Version string
	// RateLimit 为每个令牌每分钟允许的调用数，默认 120。
	RateLimit int
	Logger    *log.Logger

	mu      sync.Mutex
	buckets map[string]*bucket
}

// Handler 返回 MCP 的 HTTP 入口（POST /mcp，JSON 响应）。
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		token := bearer(r.Header.Get("Authorization"))
		if token == "" {
			writeRPCError(w, http.StatusUnauthorized, nil, -32001, "missing bearer token", nil)
			return
		}
		principal, err := s.Auth(r.Context(), token)
		if err != nil {
			status := http.StatusUnauthorized
			if !errors.Is(err, ErrUnauthorized) {
				status = http.StatusBadGateway
			}
			writeRPCError(w, status, nil, -32001, "invalid token", nil)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
		if err != nil {
			writeRPCError(w, http.StatusRequestEntityTooLarge, nil, -32600, "request too large", nil)
			return
		}
		resp, ok, status := s.Handle(r.Context(), principal, body)
		if !ok {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(resp)
	})
}

// Handle 处理一条 JSON-RPC 消息；ok=false 表示通知无需响应。
func (s *Server) Handle(ctx context.Context, p *Principal, raw []byte) ([]byte, bool, int) {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		out, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
		return out, true, http.StatusBadRequest
	}
	if req.Method == "" {
		out, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32600, Message: "invalid request"}})
		return out, true, http.StatusBadRequest
	}
	if strings.HasPrefix(req.Method, "notifications/") {
		return nil, false, http.StatusAccepted
	}
	result, rerr := s.dispatch(ctx, p, req)
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rerr}
	out, _ := json.Marshal(resp)
	return out, true, http.StatusOK
}

func (s *Server) dispatch(ctx context.Context, p *Principal, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "smartpipe-mcp", "version": s.version()},
			"instructions":    "SmartPipe 工具网关。所有操作都绑定到你的项目与角色；复杂 Issue 未获批准时 verify.patch / derivation.create 会返回 approval_required。",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		tools := ToolsFor(p.Role)
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
		}
		if !ToolAllowed(p.Role, params.Name) {
			return toolResult("工具 "+params.Name+" 不在角色 "+p.Role+" 的权限范围内", true), nil
		}
		if !s.allow(p.TokenID) {
			return nil, &rpcError{Code: -32000, Message: "rate limit exceeded"}
		}
		args, err := Arguments(params.Arguments)
		if err != nil {
			return toolResult("参数不是合法 JSON 对象: "+err.Error(), true), nil
		}
		out, callErr := s.Exec.Call(ctx, p, params.Name, args)
		if callErr != nil {
			return toolResult(callErr.Error(), true), nil
		}
		text, _ := json.MarshalIndent(out, "", "  ")
		return map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}}, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
}

func toolResult(text string, isError bool) map[string]any {
	res := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
	if isError {
		res["isError"] = true
	}
	return res
}

func (s *Server) version() string {
	if strings.TrimSpace(s.Version) == "" {
		return "0.1.0"
	}
	return s.Version
}

func (s *Server) allow(tokenID string) bool {
	limit := s.RateLimit
	if limit <= 0 {
		limit = 120
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buckets == nil {
		s.buckets = map[string]*bucket{}
	}
	b, ok := s.buckets[tokenID]
	if !ok || now.After(b.reset) {
		s.buckets[tokenID] = &bucket{count: 1, reset: now.Add(time.Minute)}
		return true
	}
	if b.count >= limit {
		return false
	}
	b.count++
	return true
}

// ServeStdio 以行分隔 JSON-RPC 运行 MCP（本地 stdio 客户端用）。
func (s *Server) ServeStdio(ctx context.Context, in io.Reader, out io.Writer, token string) error {
	principal, err := s.Auth(ctx, token)
	if err != nil {
		return fmt.Errorf("mcp: stdio auth: %w", err)
	}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	writer := bufio.NewWriter(out)
	defer writer.Flush()
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		resp, ok, _ := s.Handle(ctx, principal, []byte(line))
		if !ok {
			continue
		}
		if _, err := writer.Write(append(resp, '\n')); err != nil {
			return err
		}
		if err := writer.Flush(); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func bearer(header string) string {
	if strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return ""
}

func writeRPCError(w http.ResponseWriter, status int, id json.RawMessage, code int, msg string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg, Data: data}})
}
