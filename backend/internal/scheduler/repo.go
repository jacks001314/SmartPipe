package scheduler

import "context"

// Repo 是调度器的持久化边界。生产用 Postgres 实现，测试用内存实现。
type Repo interface {
	PutLink(ctx context.Context, l Link) error
	GetLink(ctx context.Context, msgID string) (*Link, error)

	AppendChat(ctx context.Context, m ChatMessage) error
	RecentChat(ctx context.Context, chatID string, limit int) ([]ChatMessage, error)

	CreateWorker(ctx context.Context, w *Worker) (bool, error)
	GetWorker(ctx context.Context, id string) (*Worker, error)
	ActiveWorker(ctx context.Context, projectID string, issue int) (*Worker, error)
	UpdateWorker(ctx context.Context, w *Worker) error
	ListWorkers(ctx context.Context) ([]*Worker, error)
	// ClaimNext 原子地领取一个排队中的 worker；没有名额时返回 nil, nil。
	ClaimNext(ctx context.Context, maxGlobal, maxPerProject int) (*Worker, error)
}
