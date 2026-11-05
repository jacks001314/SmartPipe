package bridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"smartpipe/backend/internal/store"
	"smartpipe/backend/internal/syntropyclient"
)

// ErrNoBinding 表示项目还没有绑定 pipeline-lead，消息不会派发。
var ErrNoBinding = errors.New("bridge: project has no pipeline-lead binding")

// Dispatcher 把新建的 Issue 派给项目的 pipeline-lead。
type Dispatcher interface {
	Dispatch(ctx context.Context, link *store.BridgeLink, project *store.Project, title string) (string, error)
}

// LeadDispatcher 按项目绑定找到 App Server 与 lead agent，再投递任务消息。
type LeadDispatcher struct {
	Store *store.Store
	// NewClient 默认返回 syntropyclient.New；测试可替换。
	NewClient func(addr string) *syntropyclient.Client
	// Timeout 单次投递超时，默认 20 秒。
	Timeout time.Duration
	// Token 是 App Server 的桌面会话令牌。
	Token string

	mu      sync.Mutex
	clients map[string]*syntropyclient.Client
}

// NewLeadDispatcher 构造派发器。
func NewLeadDispatcher(st *store.Store) *LeadDispatcher {
	return &LeadDispatcher{Store: st, Timeout: 20 * time.Second, clients: map[string]*syntropyclient.Client{}}
}

func (d *LeadDispatcher) client(addr, token string) *syntropyclient.Client {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := addr + "|" + token
	if d.clients == nil {
		d.clients = map[string]*syntropyclient.Client{}
	}
	if c, ok := d.clients[key]; ok {
		return c
	}
	var c *syntropyclient.Client
	if d.NewClient != nil {
		c = d.NewClient(addr)
	} else {
		c = syntropyclient.New(addr, token)
	}
	d.clients[key] = c
	return c
}

// Dispatch 投递任务并记录结果；没有绑定时返回 ErrNoBinding。
func (d *LeadDispatcher) Dispatch(ctx context.Context, link *store.BridgeLink, project *store.Project, title string) (string, error) {
	if link == nil || link.ProjectID == nil {
		return "", ErrNoBinding
	}
	binding, err := d.Store.GetBinding(ctx, *link.ProjectID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", ErrNoBinding
		}
		return "", err
	}
	if strings.TrimSpace(binding.LeadAgentID) == "" || strings.TrimSpace(binding.AppServerAddr) == "" {
		return "", ErrNoBinding
	}

	text := BuildLeadMessage(project, link, title)
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	sendCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	agentProject := strings.TrimSpace(binding.AgentProjectID)
	if agentProject == "" {
		agentProject = binding.ProjectID
	}
	_, sendErr := d.client(binding.AppServerAddr, d.Token).SendAgentMessage(sendCtx, agentProject, binding.LeadAgentID, text)
	if sendErr != nil {
		return binding.LeadAgentID, fmt.Errorf("bridge: dispatch to lead: %w", sendErr)
	}
	return binding.LeadAgentID, nil
}

// BuildLeadMessage 生成投递给 pipeline-lead 的任务说明。
func BuildLeadMessage(project *store.Project, link *store.BridgeLink, title string) string {
	name, projectID := "", ""
	if project != nil {
		name, projectID = project.Name, project.ID
	} else if link.ProjectID != nil {
		projectID = *link.ProjectID
	}
	var b strings.Builder
	b.WriteString("【SmartPipe 工单】收到一条新的 GitHub Issue，请作为 pipeline-lead 处理。\n\n")
	fmt.Fprintf(&b, "项目：%s（%s）\n", name, projectID)
	fmt.Fprintf(&b, "仓库：%s\n", link.Repo)
	if strings.TrimSpace(title) != "" {
		fmt.Fprintf(&b, "标题：%s\n", title)
	}
	fmt.Fprintf(&b, "Issue：#%d\n", link.IssueNumber)
	fmt.Fprintf(&b, "链接：%s\n", link.IssueURL)
	fmt.Fprintf(&b, "来源：%s chat=%s user=%s\n\n", link.Channel, link.ChatID, link.UserID)
	b.WriteString("处理要求：\n")
	b.WriteString("1. 先读 Issue 全文和评论，判断是简单缺陷还是复杂需求；\n")
	b.WriteString("2. 复杂任务必须先在 Issue 评论里向反馈人确认方案，收到 /approve vN 后再动工；\n")
	b.WriteString("3. 简单任务可直接拆分并派生 doer/test Agent，各自完成实现与自测后回报证据；\n")
	b.WriteString("4. 你在干净副本上做最终验证、合并和提交，并只由你对外回复 GitHub 与蓝信。\n")
	return b.String()
}
