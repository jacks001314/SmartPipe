package scheduler

import (
	"context"
	"time"
)

// Repo 是调度器的持久化边界。生产用 Postgres 实现，测试用内存实现。
type Repo interface {
	PutLink(ctx context.Context, l Link) error
	GetLink(ctx context.Context, msgID string) (*Link, error)

	AppendChat(ctx context.Context, m ChatMessage) error
	RecentChat(ctx context.Context, chatID string, limit int) ([]ChatMessage, error)

	ClaimMessage(ctx context.Context, messageID, botID string, now time.Time, ttl time.Duration) (bool, error)
	RecordChatReply(ctx context.Context, chatID, botID string, at time.Time) error
	RecentChatReplies(ctx context.Context, chatID string, since time.Time) (int, time.Time, error)
	CountAutoIssueDrafts(ctx context.Context, projectID, chatID string, since time.Time) (int, error)

	CreateIssueDraft(ctx context.Context, d *IssueDraft) (bool, error)
	LatestPendingIssueDraft(ctx context.Context, projectID, chatID string, now time.Time) (*IssueDraft, error)
	ClaimIssueDraft(ctx context.Context, id, projectID, chatID, actorID, actorName string, now time.Time) (*IssueDraft, error)
	FinishIssueDraft(ctx context.Context, id, status string, issue int, issueURL string, now time.Time) error
	ReleaseIssueDraft(ctx context.Context, id string, now time.Time) error
	MarkIssueDraftSource(ctx context.Context, d *IssueDraft, status string, issue int, issueURL string) error

	RecordMessage(ctx context.Context, m MessageRecord) error
	MarkMessage(ctx context.Context, platformMessageID, status, reason string, issue int, issueURL string) error
	// MarkMessageFailedIfPending 只把仍在 processing 的消息标记为失败，避免覆盖已落定的结果。
	MarkMessageFailedIfPending(ctx context.Context, platformMessageID, reason string) error
	ListMessages(ctx context.Context, limit int) ([]MessageRecord, error)

	CreateWorker(ctx context.Context, w *Worker) (bool, error)
	GetWorker(ctx context.Context, id string) (*Worker, error)
	ActiveWorker(ctx context.Context, projectID string, issue int) (*Worker, error)
	UpdateWorker(ctx context.Context, w *Worker) error
	TouchWorker(ctx context.Context, id string, at time.Time) error
	AppendWorkerEvent(ctx context.Context, ev WorkerEvent) error
	ListWorkerEvents(ctx context.Context, workerID string, limit int) ([]WorkerEvent, error)
	UpsertWorkerOutput(ctx context.Context, out *WorkerOutput) error
	ListWorkerOutputs(ctx context.Context, workerID string, afterRevision int64, limit int) ([]WorkerOutput, error)
	GetWorkerOutput(ctx context.Context, workerID, itemID string) (*WorkerOutput, error)
	ListWorkers(ctx context.Context) ([]*Worker, error)
	// ClaimNext 原子地领取一个排队中的 worker；没有名额时返回 nil, nil。
	ClaimNext(ctx context.Context, maxGlobal, maxPerProject int) (*Worker, error)
}
