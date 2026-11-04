// Package issue 实现 Issue 领域模型、存储与 HTTP API。
// 设计见 docs/自动化开发-测试-发版管道设计方案.md 3.2 节。
package issue

import "time"

// Status Issue 状态机的状态集合（见设计文档 3.2）。
type Status string

const (
	StatusNew       Status = "new"
	StatusTriaging  Status = "triaging"
	StatusNeedInfo  Status = "need_info"
	StatusTriaged   Status = "triaged"
	StatusInDev     Status = "in_dev"
	StatusTesting   Status = "testing"
	StatusReadyShip Status = "ready_to_ship"
	StatusReleased  Status = "released"
	StatusPostponed Status = "postponed"
	StatusRejected  Status = "rejected"
)

// Reporter 贡献者（M1 用蓝信用户标识，后续可选关联 App 账号）。
type Reporter struct {
	UserID   string `json:"userId"`
	UserName string `json:"userName"`
}

// Comment Issue 上的追加记录（补充信息、归并记录、状态说明）。
type Comment struct {
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
}

// Issue 领域对象。
type Issue struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"` // bug | feature | crash_report
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Reporter    Reporter  `json:"reporter"`
	Status      Status    `json:"status"`
	GroupID     string    `json:"groupId,omitempty"`
	SourceMsgID string    `json:"sourceMsgId,omitempty"`
	Comments    []Comment `json:"comments,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// CreateInput 创建入参。
type CreateInput struct {
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Reporter    Reporter `json:"reporter"`
	GroupID     string   `json:"groupId,omitempty"`
	SourceMsgID string   `json:"sourceMsgId,omitempty"`
}
