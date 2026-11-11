// Package scheduler 把蓝信消息、GitHub Issue 和 Codex worker 串成一条可追踪的流水线。
//
// 职责边界：
//   - 蓝信群是人的唯一交互入口；Issue 评论是同一会话的长期事实源；
//   - chat worker 只读，负责回答、判断是否产品缺陷、把问题落成 Issue；
//   - fix worker 只在自己的 worktree 里写代码，提交/推送/开 PR 全部走 admin-api 门禁；
//   - 每个 worker 的生命周期绑定 Issue 的 open 状态。
package scheduler

import (
	"encoding/json"
	"time"
)

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
	StatePaused         State = "paused"
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
	Platform          string       `json:"platform"`
	BotID             string       `json:"botId"`
	BotName           string       `json:"botName"`
	PlatformMessageID string       `json:"platformMessageId"`
	ChatType          string       `json:"chatType"`
	SenderID          string       `json:"senderId"`
	SenderName        string       `json:"senderName"`
	GroupID           string       `json:"groupId"`
	GroupName         string       `json:"groupName"`
	MsgType           string       `json:"msgType"`
	Text              string       `json:"text"`
	IsAtMe            bool         `json:"isAtMe"`
	ReplyToBot        bool         `json:"replyToBot,omitempty"`
	Attachments       []Attachment `json:"attachments,omitempty"`

	// Auto 标记这条消息由非 @ 观察路径触发，仅用于自动建单日限额统计。
	Auto bool `json:"-"`
}

// Attachment 是随消息一起送来的附件；数据以 base64 放在 JSON 里传输。
type Attachment struct {
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Data     []byte `json:"data"`
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
	Policy      ChatPolicy
}

// ChatPolicy 是群级响应策略。Mode=at_only 时仅处理 @、回复机器人或明确命令；
// smart 观察所有消息并按置信度响应；all 让所有消息进入完整 chat agent。
type ChatPolicy struct {
	Mode                string
	Shadow              bool
	ReplyCooldown       time.Duration
	HourlyReplyLimit    int
	AutoIssueEnabled    bool
	AutoIssueConfidence float64
	DailyAutoIssueLimit int
	AnswerConfidence    float64
}

// Worker 是一个绑定 Issue 的 Codex 执行单元。
type Worker struct {
	ID             string     `json:"id"`
	ProjectID      string     `json:"project_id"`
	ProjectName    string     `json:"project_name,omitempty"`
	Issue          int        `json:"issue_number"`
	IssueTitle     string     `json:"issue_title"`
	IssueURL       string     `json:"issue_url"`
	ChatID         string     `json:"chat_id"`
	State          State      `json:"state"`
	Branch         string     `json:"branch"`
	Worktree       string     `json:"worktree"`
	PRNumber       int        `json:"pr_number"`
	PRURL          string     `json:"pr_url"`
	Attempts       int        `json:"attempts"`
	LastError      string     `json:"last_error"`
	BlockedReason  string     `json:"blocked_reason"`
	Priority       int        `json:"priority"`
	Round          int        `json:"round"`
	Model          string     `json:"model"`
	ProviderID     string     `json:"provider_id"`
	QueuedAt       time.Time  `json:"queued_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	StateChangedAt time.Time  `json:"state_changed_at"`
	HeartbeatAt    *time.Time `json:"heartbeat_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	Running        bool       `json:"running"`
	AllowedActions []string   `json:"allowed_actions,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// MessageRecord 是一条蓝信入站消息在调度器中的处理记录。
// 直接复用 lanxin_messages 表，便于蓝信机器人和调度器控制台共享同一条消息生命周期。
type MessageRecord struct {
	ID                int64     `json:"id"`
	PlatformMessageID string    `json:"platform_message_id"`
	BotID             string    `json:"bot_id"`
	BotName           string    `json:"bot_name"`
	ChatType          string    `json:"chat_type"`
	ChatID            string    `json:"chat_id"`
	SenderID          string    `json:"user_id"`
	SenderName        string    `json:"sender_name"`
	GroupName         string    `json:"group_name"`
	MsgType           string    `json:"msg_type"`
	Text              string    `json:"text"`
	IsAtMe            bool      `json:"is_at_me"`
	ProjectID         string    `json:"project_id"`
	Status            string    `json:"status"`
	Reason            string    `json:"reason"`
	Issue             int       `json:"issue_number"`
	IssueURL          string    `json:"issue_url"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// WorkerEvent 是一次 worker 状态迁移或执行事实，供控制台时间线展示。
type WorkerEvent struct {
	ID        int64          `json:"id"`
	WorkerID  string         `json:"worker_id"`
	ProjectID string         `json:"project_id"`
	Issue     int            `json:"issue_number"`
	At        time.Time      `json:"at"`
	Kind      string         `json:"kind"`
	StateFrom string         `json:"state_from,omitempty"`
	StateTo   string         `json:"state_to,omitempty"`
	Actor     string         `json:"actor"`
	Summary   string         `json:"summary"`
	Detail    map[string]any `json:"detail,omitempty"`
}

// WorkerOutput 是 Codex 流式输出中的一个可更新 item。同一 item 的增量会合并到
// 这一条记录，前端只替换对应卡片，而不是为每个 delta 新建时间线节点。
type WorkerOutput struct {
	WorkerID  string          `json:"worker_id"`
	ItemID    string          `json:"item_id"`
	Round     int             `json:"round"`
	TurnID    string          `json:"turn_id"`
	Kind      string          `json:"kind"`
	Status    string          `json:"status"`
	Title     string          `json:"title"`
	Content   string          `json:"content,omitempty"`
	Output    string          `json:"output,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	Bytes     int             `json:"bytes"`
	Truncated bool            `json:"truncated"`
	BlobRef   string          `json:"blob_ref,omitempty"`
	Revision  int64           `json:"revision"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// OutputFrame 是输出长连接的一帧；Cursor 是下一帧的 after 游标。
type OutputFrame struct {
	Cursor  int64          `json:"cursor"`
	Items   []WorkerOutput `json:"items"`
	Running bool           `json:"running"`
}

// OutputRawPage 是完整外置日志的一页。
type OutputRawPage struct {
	Data       string `json:"data"`
	Offset     int64  `json:"offset"`
	NextOffset int64  `json:"next_offset"`
	TotalBytes int64  `json:"total_bytes"`
	HasMore    bool   `json:"has_more"`
	Missing    bool   `json:"missing,omitempty"`
}

// Overview 是调度器总览接口的返回。
type Overview struct {
	Service       string         `json:"service"`
	StartedAt     time.Time      `json:"started_at"`
	MaxGlobal     int            `json:"max_global"`
	MaxPerProject int            `json:"max_per_project"`
	Counts        map[string]int `json:"counts"`
	Workers       []*Worker      `json:"workers"`
}

// WorkerDetail 是单个 worker 及其事件时间线。
type WorkerDetail struct {
	Worker *Worker       `json:"worker"`
	Events []WorkerEvent `json:"events"`
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
