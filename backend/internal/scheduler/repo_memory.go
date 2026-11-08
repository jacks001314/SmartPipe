package scheduler

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"time"
)

// MemoryRepo 是进程内实现，供单元测试与单实例试用；语义与 Postgres 实现保持一致。
type MemoryRepo struct {
	mu      sync.Mutex
	links   map[string]Link
	chat    []ChatMessage
	workers map[string]*Worker
	nextID  int
	now     func() time.Time
}

// NewMemoryRepo 构造内存仓储。
func NewMemoryRepo() *MemoryRepo {
	return &MemoryRepo{
		links:   map[string]Link{},
		workers: map[string]*Worker{},
		now:     time.Now,
	}
}

func (r *MemoryRepo) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func copyWorker(w *Worker) *Worker {
	if w == nil {
		return nil
	}
	cp := *w
	return &cp
}

func (r *MemoryRepo) PutLink(ctx context.Context, l Link) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.links[l.PlatformMessageID]; ok {
		return nil
	}
	if l.CreatedAt.IsZero() {
		l.CreatedAt = r.clock()
	}
	r.links[l.PlatformMessageID] = l
	return nil
}

func (r *MemoryRepo) GetLink(ctx context.Context, msgID string) (*Link, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.links[msgID]
	if !ok {
		return nil, nil
	}
	return &l, nil
}

func (r *MemoryRepo) AppendChat(ctx context.Context, m ChatMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = r.clock()
	}
	m.ID = int64(len(r.chat) + 1)
	r.chat = append(r.chat, m)
	return nil
}

func (r *MemoryRepo) RecentChat(ctx context.Context, chatID string, limit int) ([]ChatMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ChatMessage, 0, limit)
	for _, m := range r.chat {
		if m.ChatID == chatID {
			out = append(out, m)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func (r *MemoryRepo) CreateWorker(ctx context.Context, w *Worker) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.workers {
		if existing.ProjectID == w.ProjectID && existing.Issue == w.Issue && existing.State != StateClosed && existing.State != StateFailed {
			return false, nil
		}
	}
	r.nextID++
	cp := copyWorker(w)
	if cp.ID == "" {
		cp.ID = "wk-" + strconv.Itoa(r.nextID)
	}
	now := r.clock()
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = now
	}
	cp.UpdatedAt = now
	if cp.State == "" {
		cp.State = StateQueued
	}
	r.workers[cp.ID] = cp
	*w = *cp
	return true, nil
}

func (r *MemoryRepo) GetWorker(ctx context.Context, id string) (*Worker, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return copyWorker(r.workers[id]), nil
}

func (r *MemoryRepo) ActiveWorker(ctx context.Context, projectID string, issue int) (*Worker, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var found *Worker
	for _, w := range r.workers {
		if w.ProjectID != projectID || w.Issue != issue {
			continue
		}
		if w.State == StateClosed || w.State == StateFailed {
			continue
		}
		if found == nil || w.UpdatedAt.After(found.UpdatedAt) {
			found = w
		}
	}
	return copyWorker(found), nil
}

func (r *MemoryRepo) UpdateWorker(ctx context.Context, w *Worker) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := copyWorker(w)
	cp.UpdatedAt = r.clock()
	r.workers[cp.ID] = cp
	*w = *cp
	return nil
}

func (r *MemoryRepo) ListWorkers(ctx context.Context) ([]*Worker, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Worker, 0, len(r.workers))
	for _, w := range r.workers {
		out = append(out, copyWorker(w))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (r *MemoryRepo) countsLocked() (int, map[string]int) {
	global := 0
	perProject := map[string]int{}
	for _, w := range r.workers {
		if w.State.IsActive() {
			global++
			perProject[w.ProjectID]++
		}
	}
	return global, perProject
}

func (r *MemoryRepo) ClaimNext(ctx context.Context, maxGlobal, maxPerProject int) (*Worker, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	global, perProject := r.countsLocked()
	if maxGlobal > 0 && global >= maxGlobal {
		return nil, nil
	}
	queued := make([]*Worker, 0)
	for _, w := range r.workers {
		if w.State == StateQueued {
			queued = append(queued, w)
		}
	}
	sort.Slice(queued, func(i, j int) bool { return queued[i].CreatedAt.Before(queued[j].CreatedAt) })
	for _, w := range queued {
		if maxPerProject > 0 && perProject[w.ProjectID] >= maxPerProject {
			continue
		}
		w.State = StatePreparing
		w.UpdatedAt = r.clock()
		return copyWorker(w), nil
	}
	return nil, nil
}
