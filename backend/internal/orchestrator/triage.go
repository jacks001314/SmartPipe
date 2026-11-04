package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"smartpipe/backend/internal/bot"
	"smartpipe/backend/internal/issue"
)

// IssueClient Orchestrator 需要的 Issue 能力（issue.Client 满足）。
type IssueClient interface {
	Create(ctx context.Context, in issue.CreateInput) (*issue.Issue, error)
	Search(ctx context.Context, q string, limit int) ([]issue.Issue, error)
	AddComment(ctx context.Context, id, author, text string) error
}

// SimilarHit 相似命中。
type SimilarHit struct {
	ID    string  `json:"id"`
	Title string  `json:"title"`
	Score float64 `json:"score"`
}

// TriageResult 归档动作结果。Action: created | merged。
type TriageResult struct {
	Action        string
	IssueID       string
	Title         string
	SimilarIssues []SimilarHit
}

// TriageEngine 归档决策接口。
// M1 为规则引擎；后续接入 Syntropy Triage Agent（意图识别/归并判断/信息补全引导）
// 时实现同一接口替换，bot 侧与 HTTP 协议不变。
type TriageEngine interface {
	Triage(ctx context.Context, msg bot.Inbound) (TriageResult, error)
}

// RuleTriage 规则引擎占位实现：文本切片 -> 相似检索 -> 归并或新建。
type RuleTriage struct {
	Issues           IssueClient
	MergeThreshold   float64
	RelatedThreshold float64
}

var _ TriageEngine = (*RuleTriage)(nil)

func (r *RuleTriage) Triage(ctx context.Context, msg bot.Inbound) (TriageResult, error) {
	text := strings.TrimSpace(msg.Text)
	title := firstLine(text)
	if title == "" {
		title = "未命名反馈"
	}
	if n := len([]rune(title)); n > 40 {
		title = string([]rune(title)[:40]) + "…"
	}
	desc := fmt.Sprintf("%s\n\n---\n来源: %s %s\n贡献者: %s (%s)\nmsgId: %s",
		text, chatTypeName(msg.ChatType), msg.GroupID, msg.SenderName, msg.SenderID, msg.PlatformMessageID)

	in := issue.CreateInput{
		Type:        "bug",
		Title:       title,
		Description: desc,
		Reporter:    issue.Reporter{UserID: msg.SenderID, UserName: msg.SenderName},
		GroupID:     msg.GroupID,
		SourceMsgID: msg.PlatformMessageID,
	}

	// 相似检索（新消息文本 vs 已有 Issue）
	var similar []SimilarHit
	if hits, err := r.Issues.Search(ctx, text, 3); err == nil {
		for _, hh := range hits {
			similar = append(similar, SimilarHit{ID: hh.ID, Title: hh.Title, Score: issue.Containment(text, hh.Title+" "+hh.Description)})
		}
	}

	// 高相似 → 归并：追加评论，不新建
	if len(similar) > 0 && similar[0].Score >= r.MergeThreshold {
		top := similar[0]
		comment := fmt.Sprintf("归并：贡献者 %s (%s) 报告了相似问题：\n%s", msg.SenderName, msg.SenderID, text)
		if err := r.Issues.AddComment(ctx, top.ID, "orchestrator", comment); err != nil {
			return TriageResult{}, fmt.Errorf("triage: merge comment: %w", err)
		}
		return TriageResult{Action: "merged", IssueID: top.ID, Title: top.Title, SimilarIssues: similar}, nil
	}

	iss, err := r.Issues.Create(ctx, in)
	if err != nil {
		return TriageResult{}, fmt.Errorf("triage: create: %w", err)
	}
	rel := make([]SimilarHit, 0)
	for _, hh := range similar {
		if hh.Score >= r.RelatedThreshold {
			rel = append(rel, hh)
		}
	}
	return TriageResult{Action: "created", IssueID: iss.ID, Title: iss.Title, SimilarIssues: rel}, nil
}

func firstLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			return strings.TrimPrefix(ln, "@")
		}
	}
	return ""
}

func chatTypeName(c string) string {
	if c == "group" {
		return "蓝信群聊"
	}
	return "蓝信私聊"
}
