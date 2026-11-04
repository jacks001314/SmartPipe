package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"

	"smartpipe/backend/internal/bot"
)

// BotClient 回复能力（通过蓝信机器人服务的内网接口发消息，凭证不出 bot 服务）。
type BotClient interface {
	Reply(ctx context.Context, chatType, targetID, refMsgID, text string) error
}

// Orchestrator 编排核心。
type Orchestrator struct {
	Tri TriageEngine
	Bot BotClient
}

// HandleInbound 处理一条归一化消息：归档 + 回复。
func (o *Orchestrator) HandleInbound(ctx context.Context, msg bot.Inbound) error {
	res, err := o.Tri.Triage(ctx, msg)
	if err != nil {
		log.Printf("[orch] triage failed: %v", err)
		_ = o.Bot.Reply(ctx, msg.ChatType, targetID(msg), msg.PlatformMessageID,
			"消息归档失败，已记录并转人工处理，请稍后重试。")
		return err
	}

	var text string
	switch res.Action {
	case "merged":
		text = fmt.Sprintf("🔁 与已有问题合并：%s\n标题：%s\n你这次的报告已追加到该 Issue，修复完成后会通知你版本号与预计发版时间（每日 06:00 发版，09:00 可更新）。",
			res.IssueID, res.Title)
	case "created":
		var sb strings.Builder
		fmt.Fprintf(&sb, "✅ 已归档 %s\n标题：%s\n状态：new（进入分诊）", res.IssueID, res.Title)
		if len(res.SimilarIssues) > 0 {
			sb.WriteString("\n相似问题：")
			for i, s := range res.SimilarIssues {
				if i > 0 {
					sb.WriteString("、")
				}
				fmt.Fprintf(&sb, "%s(%.0f%%)", s.ID, s.Score*100)
			}
		}
		sb.WriteString("\n修复完成后会通知你版本号与预计发版时间（每日 06:00 发版，09:00 可更新）。")
		text = sb.String()
	default:
		text = fmt.Sprintf("已受理：%s", res.IssueID)
	}

	if err := o.Bot.Reply(ctx, msg.ChatType, targetID(msg), msg.PlatformMessageID, text); err != nil {
		return fmt.Errorf("orch: reply via bot: %w", err)
	}
	return nil
}

func targetID(msg bot.Inbound) string {
	if msg.ChatType == "p2p" {
		return msg.SenderID
	}
	return msg.GroupID
}
