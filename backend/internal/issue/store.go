package issue

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrNotFound Issue 不存在。
var ErrNotFound = errors.New("issue: not found")

// Store 存储接口。M1 提供内存实现（demo/单机）；Postgres 实现随 M1 收尾接入
// （表结构见 deploy/postgres/schema.sql）。
type Store interface {
	Create(ctx context.Context, in CreateInput) (*Issue, error)
	Get(ctx context.Context, id string) (*Issue, error)
	Search(ctx context.Context, query string, limit int) ([]Issue, error)
	AddComment(ctx context.Context, id, author, text string) (*Issue, error)
	UpdateStatus(ctx context.Context, id string, st Status) (*Issue, error)
}

// MemoryStore 线程安全的内存实现。
type MemoryStore struct {
	mu     sync.Mutex
	seq    int
	issues map[string]*Issue
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{issues: map[string]*Issue{}}
}

func (m *MemoryStore) Create(_ context.Context, in CreateInput) (*Issue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	now := time.Now().UTC()
	iss := &Issue{
		ID:          fmt.Sprintf("ISS-%s-%04d", now.Format("200601"), m.seq),
		Type:        in.Type,
		Title:       in.Title,
		Description: in.Description,
		Reporter:    in.Reporter,
		Status:      StatusNew,
		GroupID:     in.GroupID,
		SourceMsgID: in.SourceMsgID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if iss.Type == "" {
		iss.Type = "bug"
	}
	m.issues[iss.ID] = iss
	return clone(iss), nil
}

func (m *MemoryStore) Get(_ context.Context, id string) (*Issue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	iss, ok := m.issues[id]
	if !ok {
		return nil, ErrNotFound
	}
	return clone(iss), nil
}

// Search 按 Containment 相似度降序返回（>0.15 才算命中）。
func (m *MemoryStore) Search(_ context.Context, query string, limit int) ([]Issue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	type hit struct {
		iss   Issue
		score float64
	}
	var hits []hit
	for _, iss := range m.issues {
		s := Containment(query, iss.Title+" "+iss.Description)
		if s > 0.15 {
			hits = append(hits, hit{iss: *clone(iss), score: s})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]Issue, 0, len(hits))
	for _, hh := range hits {
		out = append(out, hh.iss)
	}
	return out, nil
}

func (m *MemoryStore) AddComment(_ context.Context, id, author, text string) (*Issue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	iss, ok := m.issues[id]
	if !ok {
		return nil, ErrNotFound
	}
	iss.Comments = append(iss.Comments, Comment{Author: author, Text: text, CreatedAt: time.Now().UTC()})
	iss.UpdatedAt = time.Now().UTC()
	return clone(iss), nil
}

func (m *MemoryStore) UpdateStatus(_ context.Context, id string, st Status) (*Issue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	iss, ok := m.issues[id]
	if !ok {
		return nil, ErrNotFound
	}
	iss.Status = st
	iss.UpdatedAt = time.Now().UTC()
	return clone(iss), nil
}

func clone(i *Issue) *Issue {
	cp := *i
	cp.Comments = append([]Comment(nil), i.Comments...)
	return &cp
}

// NormalizeQuery 归一化检索串（去空白，防全空白 query）。
func NormalizeQuery(s string) string {
	return strings.TrimSpace(s)
}
