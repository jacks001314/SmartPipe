// Package releasemcp exposes the release control plane to the Codex Release
// Agent through a small, audited MCP JSON-RPC surface.
package releasemcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"smartpipe/backend/internal/releasepublish"
	"smartpipe/backend/internal/releasev2"
	"smartpipe/backend/internal/store"
)

type Server struct {
	Store            *store.Store
	Service          *releasev2.Service
	Version          string
	Objects          releasev2.ObjectStore
	Presigner        releasev2.Presigner
	Update           *releasepublish.UpdateClient
	GitHub           *releasepublish.GitHubClient
	GitHubCredential string
	UpdateURL        string
	// UpdateCredential 是升级服务调用默认使用的凭据引用；工具参数优先，
	// 其次项目发版配置里的 publishing.update_service.credential。
	UpdateCredential string
	Creds            releasepublish.Provider
	// Settings 读取界面保存的发版参数（升级服务地址、GitHub 地址、默认凭据等）；
	// nil 时只用环境变量。
	Settings SettingStore
	Clock            func() time.Time
}

func New(st *store.Store, service *releasev2.Service, version string) *Server {
	if service == nil {
		service = releasev2.NewService(st)
	}
	return &Server{Store: st, Service: service, Version: version}
}

// SettingStore 是 runtime_settings 的读取接口（*store.Store 直接满足）。
type SettingStore interface {
	GetSetting(ctx context.Context, key string) (string, error)
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type principal struct {
	Token *releasev2.Token
	Run   *releasev2.Run
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "release-mcp", "version": s.Version})
	})
	mux.HandleFunc("/mcp", s.handleMCP)
	return mux
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		writeRPCError(w, nil, -32700, "cannot read request", nil)
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil || req.Method == "" {
		writeRPCError(w, nil, -32700, "invalid JSON-RPC request", nil)
		return
	}
	if req.Method == "notifications/initialized" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	p, authErr := s.authenticate(r)
	if authErr != nil {
		writeRPCError(w, req.ID, -32001, authErr.Error(), nil)
		return
	}
	switch req.Method {
	case "initialize":
		protocol := "2025-06-18"
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &params)
		if strings.TrimSpace(params.ProtocolVersion) != "" {
			protocol = params.ProtocolVersion
		}
		writeRPC(w, req.ID, map[string]any{
			"protocolVersion": protocol,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "smartpipe-release", "version": firstNonEmpty(s.Version, "dev")},
		}, nil)
	case "ping":
		writeRPC(w, req.ID, map[string]any{}, nil)
	case "tools/list":
		writeRPC(w, req.ID, map[string]any{"tools": toolsFor(p.Token.Scopes)}, nil)
	case "tools/call":
		result, callErr := s.callTool(r.Context(), p, req.Params)
		if callErr != nil {
			writeRPC(w, req.ID, nil, &rpcError{Code: -32602, Message: callErr.Error()})
			return
		}
		writeRPC(w, req.ID, result, nil)
	default:
		writeRPC(w, req.ID, nil, &rpcError{Code: -32601, Message: "method not found"})
	}
}

func (s *Server) authenticate(r *http.Request) (*principal, error) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(header, "Bearer ") {
		return nil, errors.New("missing bearer token")
	}
	raw := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if raw == "" {
		return nil, errors.New("empty bearer token")
	}
	sum := sha256.Sum256([]byte(raw))
	token, err := s.Store.AuthenticateReleaseV2Token(r.Context(), "sha256:"+hex.EncodeToString(sum[:]))
	if err != nil {
		return nil, errors.New("invalid or expired token")
	}
	run, err := s.Service.GetRun(r.Context(), token.RunID)
	if err != nil || run.ProjectID != token.ProjectID {
		return nil, errors.New("token is not bound to a valid release run")
	}
	return &principal{Token: token, Run: run}, nil
}

type toolResult struct {
	Content           []map[string]any `json:"content"`
	StructuredContent any              `json:"structuredContent,omitempty"`
	IsError           bool             `json:"isError,omitempty"`
}

func textResult(v any) *toolResult {
	raw, _ := json.MarshalIndent(v, "", "  ")
	return &toolResult{
		Content:           []map[string]any{{"type": "text", "text": string(raw)}},
		StructuredContent: v,
	}
}

func blockedResult(code, message string) *toolResult {
	return &toolResult{
		Content:           []map[string]any{{"type": "text", "text": message}},
		StructuredContent: map[string]any{"state": "blocked", "code": code, "message": message},
		IsError:           true,
	}
}

func (s *Server) callTool(ctx context.Context, p *principal, raw json.RawMessage) (*toolResult, error) {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("invalid tools/call params: %w", err)
	}
	start := time.Now()
	result, err := s.dispatch(ctx, p, params.Name, params.Arguments)
	status := "ok"
	if err != nil {
		status = "error"
	} else if result != nil && result.IsError {
		status = "blocked"
	}
	_ = s.Store.AppendEvent(ctx, releasev2.Event{
		RunID: p.Run.ID, Actor: "agent:" + p.Token.ID, Kind: "mcp.tool", TargetID: stringArg(params.Arguments, "target"),
		Summary: params.Name, Detail: map[string]any{"status": status, "duration_ms": time.Since(start).Milliseconds(), "arguments": redactArguments(params.Arguments)},
	})
	if err != nil {
		return blockedResult("tool_error", err.Error()), nil
	}
	return result, nil
}

func (s *Server) dispatch(ctx context.Context, p *principal, name string, args map[string]any) (*toolResult, error) {
	if !allowedTool(p.Token.Scopes, name) {
		return nil, errors.New("tool is not allowed for this token")
	}
	if result, handled, err := s.dispatchWrite(ctx, p, name, args); handled {
		return result, err
	}
	// GitHub 工具是单独一组实现；漏掉这一步时工具会在 tools/list 里可见，
	// 调用却落到下面的读工具 switch，返回 unknown tool。
	if result, handled, err := s.githubDispatch(ctx, p, name, args); handled {
		return result, err
	}
	switch name {
	case "release_run_get":
		bundle, err := s.Service.GetRunBundle(ctx, p.Run.ID, int64Arg(args, "after"), intArg(args, "event_limit", 200))
		if err != nil {
			return nil, err
		}
		return textResult(bundle), nil
	case "artifact_list":
		items, err := s.Store.ListArtifacts(ctx, p.Run.ID)
		if err != nil {
			return nil, err
		}
		return textResult(items), nil
	case "executor_list":
		items, err := s.Store.ListExecutors(ctx)
		if err != nil {
			return nil, err
		}
		return textResult(items), nil
	case "release_approval_list":
		items, err := s.Store.ListApprovals(ctx, p.Run.ID, 20)
		if err != nil {
			return nil, err
		}
		return textResult(items), nil
	case "release_event_list":
		items, err := s.Store.ListEvents(ctx, p.Run.ID, int64Arg(args, "after"), intArg(args, "limit", 200))
		if err != nil {
			return nil, err
		}
		return textResult(items), nil
	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}

func toolsFor(scopes []string) []map[string]any {
	all := []map[string]any{
		{
			"name": "release_run_get", "description": "Read the current release run, targets, artifacts, publications and recent events.",
			"inputSchema": objectSchema(map[string]any{
				"after":       map[string]any{"type": "integer", "description": "Event cursor; defaults to 0."},
				"event_limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000},
			}),
		},
		{
			"name": "artifact_list", "description": "List verified artifacts registered for the current release run.",
			"inputSchema": objectSchema(map[string]any{}),
		},
		{
			"name": "executor_list", "description": "List registered Windows/macOS/Linux build executors and their capabilities.",
			"inputSchema": objectSchema(map[string]any{}),
		},
		{
			"name": "release_event_list", "description": "Read release events using a monotonically increasing cursor.",
			"inputSchema": objectSchema(map[string]any{
				"after": map[string]any{"type": "integer"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000},
			}),
		},
		{
			"name": "build_dispatch", "description": "Start (or resume) building this run's targets on their bound executors.",
			"inputSchema": objectSchema(map[string]any{}),
		},
		{
			"name": "build_wait", "description": "Wait up to timeout_ms for all targets to finish; returns per-target progress.",
			"inputSchema": objectSchema(map[string]any{
				"timeout_ms": map[string]any{"type": "integer", "minimum": 1000, "maximum": 25000},
				"poll_ms":    map[string]any{"type": "integer", "minimum": 250, "maximum": 10000},
			}),
		},
		{
			"name": "build_logs", "description": "Read streamed build logs for one target or the whole run.",
			"inputSchema": objectSchema(map[string]any{
				"target":    map[string]any{"type": "string"},
				"after":     map[string]any{"type": "integer", "description": "Last event cursor you received."},
				"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": 500},
				"max_bytes": map[string]any{"type": "integer", "minimum": 1024, "maximum": 262144},
			}),
		},
		{
			"name": "build_retry", "description": "Requeue a failed target for another executor attempt.",
			"inputSchema": objectSchemaRequired(map[string]any{
				"target": map[string]any{"type": "string"},
				"reason": map[string]any{"type": "string"},
			}, []string{"target"}),
		},
		{
			"name": "release_notes_save", "description": "Save the release notes used by GitHub and the channel manifest.",
			"inputSchema": objectSchemaRequired(map[string]any{
				"notes": map[string]any{"type": "string", "maxLength": 100000},
			}, []string{"notes"}),
		},
		{
			"name": "release_approval_list", "description": "List publish approvals for the current run, including expiry and bound content hash.",
			"inputSchema": objectSchema(map[string]any{}),
		},
		{
			"name": "artifact_verify", "description": "Verify every required target produced its declared artifact roles and that stored digests match.",
			"inputSchema": objectSchema(map[string]any{}),
		},
		{
			"name": "update_bundle_build", "description": "Generate the canonical upgrade-bundle.json from verified artifacts and store it in the artifact library.",
			"inputSchema": objectSchema(map[string]any{}),
		},
		{
			"name": "update_import_stage", "description": "Create unpublished component releases and upload every bundle artifact to the upgrade service. Does not move any channel pointer.",
			"inputSchema": objectSchema(map[string]any{
				"credential": map[string]any{"type": "string", "description": "Credential reference configured on the server."},
				"channel":    map[string]any{"type": "string"},
			}),
		},
		{
			"name": "update_import_get", "description": "Read a staged release with its artifact matrix plus the channel pointer clients currently receive.",
			"inputSchema": objectSchema(map[string]any{
				"credential": map[string]any{"type": "string"},
				"import_id":  map[string]any{"type": "string"},
			}),
		},
		{
			"name": "update_channel_promote", "description": "Point every staged component at the run channel, publish the channel BOM and verify the signed distribution. Compare-and-swap expectations are optional, so do not guess version numbers.",
			"inputSchema": objectSchema(map[string]any{
				"credential":               map[string]any{"type": "string", "description": "Credential reference; defaults to the project publishing.update_service.credential."},
				"expected_current_version": map[string]any{"type": "string", "description": "Optional CAS expectation; omit to use the current channel pointer."},
				"expected_bom_sha256":      map[string]any{"type": "string", "description": "Optional CAS expectation; omit to use the current channel pointer."},
			}),
		},
		{
			"name": "github_draft_create", "description": "Create a draft GitHub release for this run using the saved notes.",
			"inputSchema": objectSchema(map[string]any{
				"credential": map[string]any{"type": "string"},
				"tag":        map[string]any{"type": "string"},
				"name":       map[string]any{"type": "string"},
				"body":       map[string]any{"type": "string"},
			}),
		},
		{
			"name": "github_asset_upload", "description": "Stream one verified artifact into the draft GitHub release. Select the artifact by artifact_id, by file name (artifact) or by role; re-uploading an asset that already exists with the same size is a no-op.",
			"inputSchema": objectSchema(map[string]any{
				"credential":  map[string]any{"type": "string"},
				"release_id":  map[string]any{"type": "integer", "description": "Defaults to the draft created for this run."},
				"artifact_id": map[string]any{"type": "integer", "description": "Preferred selector for the artifact row."},
				"artifact":    map[string]any{"type": "string", "description": "Artifact file name; alternative to artifact_id."},
				"role":        map[string]any{"type": "string", "description": "Artifact role; alternative to artifact_id."},
			}),
		},
		{
			"name": "github_release_publish", "description": "Publish the draft GitHub release after verifying its assets.",
			"inputSchema": objectSchema(map[string]any{
				"credential": map[string]any{"type": "string"},
				"release_id": map[string]any{"type": "integer"},
			}),
		},
		{
			"name": "github_release_get", "description": "Read the GitHub release and its uploaded asset digests.",
			"inputSchema": objectSchema(map[string]any{
				"credential": map[string]any{"type": "string"},
				"release_id": map[string]any{"type": "integer"},
			}),
		},
		{
			"name": "release_publish", "description": "Final publish entry. Re-verifies targets, checks the approval gate and promotes the staged channel pointer. CAS expectations are optional; the server reads the current pointer when they are omitted.",
			"inputSchema": objectSchema(map[string]any{
				"credential":               map[string]any{"type": "string", "description": "Credential reference; defaults to the project publishing.update_service.credential."},
				"expected_current_version": map[string]any{"type": "string", "description": "Optional CAS expectation; omit to use the current channel pointer."},
				"expected_bom_sha256":      map[string]any{"type": "string", "description": "Optional CAS expectation; omit to use the current channel pointer."},
			}),
		},
	}
	out := make([]map[string]any, 0, len(all))
	for _, tool := range all {
		name, _ := tool["name"].(string)
		if allowedTool(scopes, name) {
			out = append(out, tool)
		}
	}
	return out
}

func objectSchema(props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props, "additionalProperties": false}
}

func objectSchemaRequired(props map[string]any, required []string) map[string]any {
	schema := objectSchema(props)
	schema["required"] = required
	return schema
}

func allowedTool(scopes []string, name string) bool {
	switch name {
	case "release_run_get", "release_event_list":
		return releasev2.HasScope(scopes, "release.read")
	case "artifact_list":
		return releasev2.HasScope(scopes, "artifact.read")
	case "executor_list":
		return releasev2.HasScope(scopes, "executor.read")
	case "build_dispatch", "build_wait":
		return releasev2.HasScope(scopes, "build.read") || releasev2.HasScope(scopes, "build.write")
	case "build_retry", "release_notes_save":
		return releasev2.HasScope(scopes, "release.write")
	case "build_logs":
		return releasev2.HasScope(scopes, "build.read") || releasev2.HasScope(scopes, "build.write") || releasev2.HasScope(scopes, "release.read")
	case "release_approval_list":
		return releasev2.HasScope(scopes, "release.read")
	case "artifact_verify":
		return releasev2.HasScope(scopes, "artifact.read") || releasev2.HasScope(scopes, "artifact.write")
	case "update_bundle_build":
		return releasev2.HasScope(scopes, "artifact.write")
	case "update_import_stage":
		return releasev2.HasScope(scopes, "update.write")
	case "update_import_get":
		return releasev2.HasScope(scopes, "update.read") || releasev2.HasScope(scopes, "update.write")
	case "update_channel_promote":
		return releasev2.HasScope(scopes, "update.publish")
	case "github_draft_create", "github_asset_upload", "github_release_publish":
		return releasev2.HasScope(scopes, "github.write")
	case "github_release_get":
		return releasev2.HasScope(scopes, "github.read") || releasev2.HasScope(scopes, "github.write")
	case "release_publish":
		return releasev2.HasScope(scopes, "release.publish")
	default:
		return false
	}
}

func redactArguments(args map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range args {
		lower := strings.ToLower(k)
		if strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") {
			out[k] = "[redacted]"
		} else {
			out[k] = v
		}
	}
	return out
}

func asString(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func asStringSlice(v any) []string {
	switch value := v.(type) {
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if s := asString(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return value
	default:
		return nil
	}
}

func int64Arg(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case json.Number:
		n, _ := v.Int64()
		return n
	default:
		return 0
	}
}

func intArg(args map[string]any, key string, def int) int {
	n := int(int64Arg(args, key))
	if n <= 0 {
		return def
	}
	return n
}

func stringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any, rpcErr *rpcError) {
	writeJSON(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: id, Result: result, Error: rpcErr})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string, data any) {
	writeRPC(w, id, nil, &rpcError{Code: code, Message: message, Data: data})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
