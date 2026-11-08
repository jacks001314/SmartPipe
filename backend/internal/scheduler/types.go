// Package scheduler 把蓝信消息、GitHub Issue 和 Codex worker 串成一条可追踪的流水线。
//
// 职责边界：
//   - 蓝信群是人的唯一交互入口；Issue 评论是同一会话的长期事实源；
//   - chat worker 只读，负责回答、判断是否产品缺陷、把问题落成 Issue；
//   - fix worker 只在自己的 worktree 里写代码，提交/推送/开 PR 全部走 admin-api 门禁；
//   - 每个 worker 的生命周期绑定 Issue 的 open 状态。
package scheduler

import "time"

// State 是 worker 的状态机节点，与文档 docs/调度器工作流设计.md 一致。
type State string

const (
	StateQueued         State = "queued"
	StatePreparing      State = "preparing"
	StateCoding         State = "coding"
	StateVerifying      State = "verifying"
	StatePushing        State = "pushing"
	StateAwaitingInput  State = "awaiting_input"
	StateAwaitingReview State = "awaiting_review"
	StateBlocked        State = "blocked"
	StateClosed         State = "closed"
	StateFailed         State = "failed"
)

// ActiveStates 是占用并发名额的状态集合；等人工的状态不占名额。
func ActiveStates() []State {
	return []State{StatePreparing, StateCoding, StateVerifying, StatePushing}
}

// IsActive 判断该状态是否占用并发名额。
func (s State) IsActive() bool {
	for _, v := range ActiveStates() {
		if s == v {
			return true
		}
	}
	return false
}

// Inbound 是 bot 归一化后的蓝信入站消息（JSON 字段与 bot.Inbound 对齐）。
type Inbound struct {
	Platform          string `json:"platform"`
	BotID             string `json:"botId"`
	BotName           string `json:"botName"`
	PlatformMessageID string `json:"platformMessageId"`
	ChatType          string `json:"chatType"`
	SenderID          string `json:"senderId"`
	SenderName        string `json:"senderName"`
	GroupID           string `json:"groupId"`
	GroupName         string `json:"groupName"`
	MsgType           string `json:"msgType"`
	Text              string `json:"text"`
	IsAtMe            bool   `json:"isAtMe"`
}

// ChatID 返回用于会话归属的 ID：群消息取群，私聊取用户。
func (m Inbound) ChatID() string {
	if m.GroupID != "" {
		return m.GroupID
	}
	return m.SenderID
}

// Project 是调度器需要的最小项目视图。
type Project struct {
	ID          string
	Name        string
	Repo        string // owner/name 或完整 URL
	IssueRepo   string
	DefaultBase string
	LocalPath   string // 已克隆的本地仓库，用于创建 worktree
	Model       string
	Effort      string
	ProviderID  string
	GroupIDs    []string
}

// Worker 是一个绑定 Issue 的 Codex 执行单元。
type Worker struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Issue     int       `json:"issue_number"`
	IssueURL  string    `json:"issue_url"`
	ChatID    string    `json:"chat_id"`
	State     State     `json:"state"`
	Branch    string    `json:"branch"`
	Worktree  string    `json:"worktree"`
	PRNumber  int       `json:"pr_number"`
	Attempts  int       `json:"attempts"`
	LastError string    `json:"last_error"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ChatMessage 是蓝信会话的一条历史，供 chat worker 组装上下文。
type ChatMessage struct {
	ID        int64
	ChatID    string
	ProjectID string
	Role      string // user | assistant
	Body      string
	CreatedAt time.Time
}

// Link 记录蓝信消息与 Issue 的映射，用于幂等和回链。
type Link struct {
	PlatformMessageID string
	ProjectID         string
	Issue             int
	ChatID            string
	CreatedAt         time.Time
}

// Issue 是 GitHub Issue 的最小视图。
type Issue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	URL    string `json:"url"`
	Labels []string
}

// Comment 是 Issue/PR 下的一条评论。
type Comment struct {
	ID        int64     `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// PullRequest 是 PR 的最小视图。
type PullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"`
	Merged bool   `json:"merged"`
}

// IssueEvent 是 GitHub webhook 转发给调度器的事件。
type IssueEvent struct {
	Event       string `json:"event"`  // issue_comment | issues
	Action      string `json:"action"` // created | closed | reopened
	ProjectID   string `json:"project_id"`
	Repo        string `json:"repo"`
	Issue       int    `json:"issue_number"`
	CommentID   int64  `json:"comment_id"`
	Author      string `json:"author"`
	Association string `json:"association"`
	Body        string `json:"body"`
	Title       string `json:"title"`
	State       string `json:"state"`
}
