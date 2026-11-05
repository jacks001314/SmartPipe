// Package syntropyclient 是 SmartPipe 访问 Syntropy App Server 的最小 HTTP 客户端。
//
// 只走 App Server 已公开的 HTTP 接口，不修改 Syntropy 源码：
//   - GET  /api/health                 进程与 Master 依赖状态（未配置 Master 时返回 503）
//   - GET  /api/master/status|registry 控制面状态
//   - POST /api/master/deploy         部署 Master（local 或 ssh）
//   - GET  /api/node/list/{page}/{size}
//   - POST /api/node/create           加入 Worker
//   - GET  /api/agents                列出 Agent
//   - POST /api/agents                部署 pipeline-lead（protojson: spec + requestId）
//   - GET  /api/agent-operations/{id} 查询部署操作
//
// 鉴权：当前 App Server 只支持桌面会话 cookie（syntropy_desktop_session），
// 正式服务令牌仍是平台缺口；这里同时带上 Bearer 以便上游补齐后直接切换。
package syntropyclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SessionCookie 是 App Server 的桌面会话 cookie 名。
const SessionCookie = "syntropy_desktop_session"

// ErrUnauthorized 表示 App Server 拒绝了会话凭证。
var ErrUnauthorized = errors.New("syntropyclient: unauthorized")

// StatusError 包装非 2xx 响应。
type StatusError struct {
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 300 {
		body = body[:300] + "..."
	}
	return fmt.Sprintf("syntropyclient: HTTP %d: %s", e.Status, body)
}

// Client 是 App Server 的 HTTP 客户端。
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New 构造客户端；baseURL 形如 http://127.0.0.1:8765。
func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Token:   strings.TrimSpace(token),
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Raw 是未定型 JSON 对象的通用表示，便于透传新字段给前端。
type Raw map[string]any

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	u := c.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Cookie", SessionCookie+"="+c.Token)
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return req, nil
}

// Do 执行请求并返回状态码与响应体；非 2xx 不视为错误，由调用方决定。
func (c *Client) Do(ctx context.Context, method, path string, payload any) (int, []byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, fmt.Errorf("syntropyclient: encode body: %w", err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return 0, nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("syntropyclient: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, data, nil
}

// GetJSON 执行 GET 并把 2xx 响应解码为 Raw。
func (c *Client) GetJSON(ctx context.Context, path string) (Raw, error) {
	status, data, err := c.Do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	if status < 200 || status > 299 {
		return nil, &StatusError{Status: status, Body: string(data)}
	}
	out := Raw{}
	if len(bytes.TrimSpace(data)) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("syntropyclient: decode %s: %w", path, err)
	}
	return out, nil
}

// PostJSON 执行 POST 并解码 JSON 响应。
func (c *Client) PostJSON(ctx context.Context, path string, payload any) (Raw, error) {
	status, data, err := c.Do(ctx, http.MethodPost, path, payload)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	if status < 200 || status > 299 {
		return nil, &StatusError{Status: status, Body: string(data)}
	}
	out := Raw{}
	if len(bytes.TrimSpace(data)) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("syntropyclient: decode %s: %w", path, err)
	}
	return out, nil
}

// Health 读取 /api/health。Master 未配置时服务端返回 503，仍视为进程存活。
func (c *Client) Health(ctx context.Context) (Raw, int, error) {
	status, data, err := c.Do(ctx, http.MethodGet, "/api/health", nil)
	if err != nil {
		return nil, 0, err
	}
	if status == http.StatusUnauthorized {
		return nil, status, ErrUnauthorized
	}
	out := Raw{}
	if len(bytes.TrimSpace(data)) > 0 {
		if jsonErr := json.Unmarshal(data, &out); jsonErr != nil {
			return nil, status, fmt.Errorf("syntropyclient: decode health: %w", jsonErr)
		}
	}
	if status >= 500 && out["status"] == nil {
		return out, status, &StatusError{Status: status, Body: string(data)}
	}
	return out, status, nil
}

// MasterStatus 返回控制面状态。
func (c *Client) MasterStatus(ctx context.Context) (Raw, error) {
	return c.GetJSON(ctx, "/api/master/status")
}

// MasterRegistry 返回已注册的 Master 列表。
func (c *Client) MasterRegistry(ctx context.Context) (Raw, error) {
	return c.GetJSON(ctx, "/api/master/registry")
}

// MasterDeployRequest 对应 /api/master/deploy 的请求体。
type MasterDeployRequest struct {
	Name           string         `json:"name"`
	Target         string         `json:"target"`
	SSH            map[string]any `json:"ssh,omitempty"`
	OS             string         `json:"os,omitempty"`
	Arch           string         `json:"arch,omitempty"`
	PubIP          string         `json:"pubIP,omitempty"`
	Domain         string         `json:"domain,omitempty"`
	GrpcPort       int            `json:"grpcPort,omitempty"`
	GatewayRPCPort int            `json:"gatewayRPCPort,omitempty"`
}

// DeployMaster 触发 Master 部署。
func (c *Client) DeployMaster(ctx context.Context, in MasterDeployRequest) (Raw, error) {
	return c.PostJSON(ctx, "/api/master/deploy", in)
}

// DeleteMaster 移除本地 Master 部署（DELETE /api/master）。
func (c *Client) DeleteMaster(ctx context.Context, id string) error {
	path := "/api/master"
	if strings.TrimSpace(id) != "" {
		path += "?id=" + url.QueryEscape(id)
	}
	status, data, err := c.Do(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if status < 200 || status > 299 {
		return &StatusError{Status: status, Body: string(data)}
	}
	return nil
}

// NodeList 返回节点分页列表，masterID 为空时使用当前 active Master。
func (c *Client) NodeList(ctx context.Context, masterID string, page, size int) (Raw, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 50
	}
	path := fmt.Sprintf("/api/node/list/%d/%d", page, size)
	if masterID != "" {
		path += "?masterId=" + url.QueryEscape(masterID)
	}
	return c.GetJSON(ctx, path)
}

// NodeCreate 加入一个 Worker/节点。
func (c *Client) NodeCreate(ctx context.Context, payload Raw) (Raw, error) {
	return c.PostJSON(ctx, "/api/node/create", payload)
}

// NodeDeploymentProgress 查询节点部署进度。
func (c *Client) NodeDeploymentProgress(ctx context.Context, operationID, masterID string) (Raw, error) {
	path := "/api/node/deployments/" + url.PathEscape(operationID)
	if masterID != "" {
		path += "?masterId=" + url.QueryEscape(masterID)
	}
	return c.GetJSON(ctx, path)
}

// AgentList 返回 Agent 列表。
func (c *Client) AgentList(ctx context.Context) ([]Raw, error) {
	out, err := c.GetJSON(ctx, "/api/agents")
	if err != nil {
		return nil, err
	}
	items, _ := out["agents"].([]any)
	agents := make([]Raw, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			agents = append(agents, Raw(m))
		}
	}
	return agents, nil
}

// AgentSpec 是部署 pipeline-lead 所需的字段子集（protojson 接受 snake_case）。
type AgentSpec struct {
	ProjectID    string            `json:"project_id"`
	Name         string            `json:"name"`
	DisplayName  string            `json:"display_name,omitempty"`
	TargetNodeID string            `json:"target_node_id,omitempty"`
	Runtime      string            `json:"runtime,omitempty"`
	TemplateName string            `json:"template_name,omitempty"`
	DesiredState string            `json:"desired_state,omitempty"`
	Workspace    string            `json:"workspace,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
}

// DeployAgent 调用 POST /api/agents，返回 AgentOperation。
func (c *Client) DeployAgent(ctx context.Context, spec AgentSpec, requestID string) (Raw, error) {
	if spec.Runtime == "" {
		spec.Runtime = "AGENT_RUNTIME_TYPE_CODEX"
	}
	if spec.DesiredState == "" {
		spec.DesiredState = "AGENT_DESIRED_STATE_RUNNING"
	}
	body := Raw{"spec": spec}
	if requestID != "" {
		body["requestId"] = requestID
	}
	return c.PostJSON(ctx, "/api/agents", body)
}

// TemplateList 返回 Agent 模板摘要列表（GET /api/agent-templates）。
func (c *Client) TemplateList(ctx context.Context) ([]Raw, error) {
	out, err := c.GetJSON(ctx, "/api/agent-templates")
	if err != nil {
		return nil, err
	}
	items, _ := out["templates"].([]any)
	list := make([]Raw, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			list = append(list, Raw(m))
		}
	}
	return list, nil
}

// TemplateCreate 创建或覆盖一个 Agent 模板（POST /api/agent-templates）。
func (c *Client) TemplateCreate(ctx context.Context, settings Raw) (Raw, error) {
	return c.PostJSON(ctx, "/api/agent-templates", settings)
}

// TemplateGet 读取单个模板的完整配置。
func (c *Client) TemplateGet(ctx context.Context, name string) (Raw, error) {
	return c.GetJSON(ctx, "/api/agent-templates/"+url.PathEscape(name))
}

// AgentOperation 查询一次 Agent 操作。
func (c *Client) AgentOperation(ctx context.Context, id string) (Raw, error) {
	return c.GetJSON(ctx, "/api/agent-operations/"+url.PathEscape(id))
}

// SetAgentDesiredState 启停 Agent。
func (c *Client) SetAgentDesiredState(ctx context.Context, id, state string) (Raw, error) {
	return c.PostJSON(ctx, "/api/agents/"+url.PathEscape(id)+"/desired-state", Raw{"desiredState": state})
}

// DeleteAgent 回收 Agent。
func (c *Client) DeleteAgent(ctx context.Context, id string) error {
	status, data, err := c.Do(ctx, http.MethodDelete, "/api/agents/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if status < 200 || status > 299 {
		return &StatusError{Status: status, Body: string(data)}
	}
	return nil
}
