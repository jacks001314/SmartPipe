package scheduler

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"sync"
	"time"
)

// MemoryRepo 是进程内实现，供单元测试与单实例试用；语义与 Postgres 实现保持一致。
type MemoryRepo struct {
	mu           sync.Mutex
	links        map[string]Link
	chat         []ChatMessage
	workers      map[string]*Worker
	events       []WorkerEvent
	messages     []MessageRecord
	drafts       map[string]*IssueDraft
	claims       map[string]time.Time
	replies      []chatReplyRecord
	outputs      map[string]map[string]WorkerOutput
	nextID       int
	nextOutputID int64
	now          func() time.Time
}

// NewMemoryRepo 构造内存仓储。
func NewMemoryRepo() *MemoryRepo {
	return &MemoryRepo{
		links:   map[string]Link{},
		workers: map[string]*Worker{},
		drafts:  map[string]*IssueDraft{},
		claims:  map[string]time.Time{},
		outputs: map[string]map[string]WorkerOutput{},
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

func (r *MemoryRepo) RecordMessage(ctx context.Context, m MessageRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock()
	for i := range r.messages {
		if r.messages[i].PlatformMessageID == m.PlatformMessageID {
			r.messages[i] = m
			r.messages[i].UpdatedAt = now
			return nil
		}
	}
	m.ID = int64(len(r.messages) + 1)
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	m.UpdatedAt = now
	r.messages = append(r.messages, m)
	return nil
}

func (r *MemoryRepo) MarkMessage(ctx context.Context, platformMessageID, status, reason string, issue int, issueURL string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.messages {
		if r.messages[i].PlatformMessageID == platformMessageID {
			r.messages[i].Status = status
			r.messages[i].Reason = reason
			r.messages[i].Issue = issue
			r.messages[i].IssueURL = issueURL
			r.messages[i].UpdatedAt = r.clock()
			return nil
		}
	}
	return nil
}

func (r *MemoryRepo) ListMessages(ctx context.Context, limit int) ([]MessageRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]MessageRecord, len(r.messages))
	copy(out, r.messages)
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
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
	if cp.QueuedAt.IsZero() {
		cp.QueuedAt = now
	}
	if cp.StateChangedAt.IsZero() {
		cp.StateChangedAt = now
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
	now := r.clock()
	if cp.StateChangedAt.IsZero() {
		cp.StateChangedAt = now
	}
	cp.UpdatedAt = now
	r.workers[cp.ID] = cp
	*w = *cp
	return nil
}

func (r *MemoryRepo) TouchWorker(ctx context.Context, id string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.workers[id]
	if !ok {
		return nil
	}
	if at.IsZero() {
		at = r.clock()
	}
	w.HeartbeatAt = &at
	w.UpdatedAt = at
	return nil
}

func (r *MemoryRepo) AppendWorkerEvent(ctx context.Context, ev WorkerEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ev.At.IsZero() {
		ev.At = r.clock()
	}
	ev.ID = int64(len(r.events) + 1)
	r.events = append(r.events, ev)
	return nil
}

func (r *MemoryRepo) ListWorkerEvents(ctx context.Context, workerID string, limit int) ([]WorkerEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]WorkerEvent, 0)
	for _, ev := range r.events {
		if ev.WorkerID == workerID {
			out = append(out, ev)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func (r *MemoryRepo) UpsertWorkerOutput(ctx context.Context, out *WorkerOutput) error {
	if out == nil || out.WorkerID == "" || out.ItemID == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock()
	r.nextOutputID++
	if out.CreatedAt.IsZero() {
		out.CreatedAt = now
	}
	out.UpdatedAt = now
	out.Revision = r.nextOutputID
	if r.outputs[out.WorkerID] == nil {
		r.outputs[out.WorkerID] = map[string]WorkerOutput{}
	}
	if old, ok := r.outputs[out.WorkerID][out.ItemID]; ok && !old.CreatedAt.IsZero() {
		out.CreatedAt = old.CreatedAt
	}
	r.outputs[out.WorkerID][out.ItemID] = *out
	return nil
}

func (r *MemoryRepo) ListWorkerOutputs(ctx context.Context, workerID string, afterRevision int64, limit int) ([]WorkerOutput, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	out := make([]WorkerOutput, 0)
	for _, item := range r.outputs[workerID] {
		if item.Revision > afterRevision {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Revision < out[j].Revision })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *MemoryRepo) GetWorkerOutput(ctx context.Context, workerID, itemID string) (*WorkerOutput, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.outputs[workerID][itemID]
	if !ok {
		return nil, nil
	}
	cp := item
	cp.Metadata = append(json.RawMessage(nil), item.Metadata...)
	return &cp, nil
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
	sort.SliceStable(queued, func(i, j int) bool {
		if queued[i].Priority != queued[j].Priority {
			return queued[i].Priority > queued[j].Priority
		}
		if !queued[i].QueuedAt.Equal(queued[j].QueuedAt) {
			return queued[i].QueuedAt.Before(queued[j].QueuedAt)
		}
		return queued[i].CreatedAt.Before(queued[j].CreatedAt)
	})
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

func copyIssueDraft(d *IssueDraft) *IssueDraft {
	if d == nil {
		return nil
	}
	cp := *d
	cp.Labels = append([]string(nil), d.Labels...)
	return &cp
}

func (r *MemoryRepo) CreateIssueDraft(ctx context.Context, d *IssueDraft) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d.SourceMessageID != "" {
		for _, existing := range r.drafts {
			if existing.SourceMessageID == d.SourceMessageID {
				return false, nil
			}
		}
	}
	now := r.clock()
	cp := copyIssueDraft(d)
	if cp.Status == "" {
		cp.Status = IssueDraftPending
	}
	if cp.Kind == "" {
		cp.Kind = "chat"
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = now
	}
	cp.UpdatedAt = now
	if cp.ExpiresAt.IsZero() {
		cp.ExpiresAt = now.Add(issueDraftTTL)
	}
	if cp.ID == "" {
		return false, ErrIssueDraftNotFound
	}
	for _, existing := range r.drafts {
		if existing.ProjectID == cp.ProjectID && existing.ChatID == cp.ChatID && existing.Status == IssueDraftPending {
			existing.Status = IssueDraftSuperseded
			existing.UpdatedAt = now
		}
	}
	r.drafts[cp.ID] = cp
	*d = *cp
	return true, nil
}

func (r *MemoryRepo) LatestPendingIssueDraft(ctx context.Context, projectID, chatID string, now time.Time) (*IssueDraft, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var found *IssueDraft
	for _, d := range r.drafts {
		if d.ProjectID != projectID || d.ChatID != chatID || d.Status != IssueDraftPending {
			continue
		}
		if !d.ExpiresAt.After(now) {
			d.Status = IssueDraftExpired
			d.UpdatedAt = now
			continue
		}
		if found == nil || d.CreatedAt.After(found.CreatedAt) {
			found = d
		}
	}
	return copyIssueDraft(found), nil
}

func (r *MemoryRepo) ClaimIssueDraft(ctx context.Context, id, projectID, chatID, actorID, actorName string, now time.Time) (*IssueDraft, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.drafts[id]
	if d == nil || d.ProjectID != projectID || d.ChatID != chatID || d.Status != IssueDraftPending || !d.ExpiresAt.After(now) {
		return nil, nil
	}
	d.Status = IssueDraftCreating
	d.ConfirmedByID = actorID
	d.ConfirmedByName = actorName
	d.UpdatedAt = now
	return copyIssueDraft(d), nil
}

func (r *MemoryRepo) FinishIssueDraft(ctx context.Context, id, status string, issue int, issueURL string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.drafts[id]
	if d == nil {
		return ErrIssueDraftNotFound
	}
	d.Status = status
	d.Issue = issue
	d.IssueURL = issueURL
	d.UpdatedAt = now
	return nil
}

func (r *MemoryRepo) ReleaseIssueDraft(ctx context.Context, id string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.drafts[id]
	if d == nil || d.Status != IssueDraftCreating {
		return nil
	}
	d.Status = IssueDraftPending
	d.UpdatedAt = now
	return nil
}

// MarkIssueDraftSource 在内存仓储里无需回写外部事实；PostgreSQL 实现会更新崩溃上报状态。
func (r *MemoryRepo) MarkIssueDraftSource(ctx context.Context, d *IssueDraft, status string, issue int, issueURL string) error {
	return nil
}

type chatReplyRecord struct {
	chatID string
	botID  string
	at     time.Time
}

func (r *MemoryRepo) ClaimMessage(ctx context.Context, messageID, botID string, now time.Time, ttl time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if until, ok := r.claims[messageID]; ok && until.After(now) {
		return false, nil
	}
	r.claims[messageID] = now.Add(ttl)
	return true, nil
}

func (r *MemoryRepo) RecordChatReply(ctx context.Context, chatID, botID string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replies = append(r.replies, chatReplyRecord{chatID: chatID, botID: botID, at: at})
	return nil
}

func (r *MemoryRepo) RecentChatReplies(ctx context.Context, chatID string, since time.Time) (int, time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	var last time.Time
	for _, item := range r.replies {
		if item.chatID != chatID || item.at.Before(since) {
			continue
		}
		count++
		if item.at.After(last) {
			last = item.at
		}
	}
	return count, last, nil
}

func (r *MemoryRepo) CountAutoIssueDrafts(ctx context.Context, projectID, chatID string, since time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, d := range r.drafts {
		if d.ProjectID == projectID && d.ChatID == chatID && d.Auto && !d.CreatedAt.Before(since) {
			count++
		}
	}
	return count, nil
}
