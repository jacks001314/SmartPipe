package syntropyclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// SendAgentMessage 把一条文本消息投递给指定 Agent：
// POST /api/agents/{id}/messages，body {projectId, text}。
// 返回 App Server 的原始响应（含 message_id 等），便于审计。
func (c *Client) SendAgentMessage(ctx context.Context, projectID, agentID, text string) (Raw, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, fmt.Errorf("syntropyclient: agent id is required")
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("syntropyclient: message text is required")
	}
	body := Raw{"text": text}
	if strings.TrimSpace(projectID) != "" {
		body["projectId"] = strings.TrimSpace(projectID)
	}
	status, data, err := c.Do(ctx, http.MethodPost, "/api/agents/"+url.PathEscape(agentID)+"/messages", body)
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
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &out); err != nil {
			return out, nil
		}
	}
	return out, nil
}
