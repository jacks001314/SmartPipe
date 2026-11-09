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

	RecordMessage(ctx context.Context, m MessageRecord) error
	MarkMessage(ctx context.Context, platformMessageID, status, reason string, issue int, issueURL string) error
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
