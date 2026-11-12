package releasev2

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Repository interface {
	CreateReleaseV2Plan(context.Context, *Plan) error
	GetReleaseV2Plan(context.Context, string) (*Plan, error)
	CreateRunFromPlan(context.Context, *Run, []Target) (*Run, error)
	GetRun(context.Context, string) (*Run, error)
	ListRuns(context.Context, string, int) ([]Run, error)
	ListTargets(context.Context, string) ([]Target, error)
	ListArtifacts(context.Context, string) ([]Artifact, error)
	ListPublications(context.Context, string) ([]Publication, error)
	ListEvents(context.Context, string, int64, int) ([]Event, error)
	UpdateRunStatus(context.Context, string, RunStatus, RunStatus, string, string) (bool, error)
	AppendEvent(context.Context, Event) error
	ListExecutors(context.Context) ([]Executor, error)
	GetTarget(context.Context, string, string) (*Target, error)
	RetryReleaseV2Target(context.Context, string, string) (bool, error)
	UpdateRunNotes(context.Context, string, string, string) (bool, error)
	AppendLogChunk(context.Context, string, string, string, int, string) error
	ListLogChunks(context.Context, string, string, int64, int) ([]Event, error)
}

type RunBundle struct {
	Run          *Run          `json:"run"`
	Targets      []Target      `json:"targets"`
	Artifacts    []Artifact    `json:"artifacts"`
	Publications []Publication `json:"publications,omitempty"`
	Events       []Event       `json:"events"`
	Blockers     []Blocker     `json:"blockers,omitempty"`
}

type Service struct {
	repo Repository
	now  func() time.Time
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) WithClock(now func() time.Time) *Service {
	if now != nil {
		s.now = now
	}
	return s
}

func (s *Service) CreatePlan(ctx context.Context, req PlanRequest) (*Plan, error) {
	plan, err := BuildPlan(req, s.now())
	if err != nil {
		return nil, err
	}
	if err := s.repo.CreateReleaseV2Plan(ctx, plan); err != nil {
		return nil, err
	}
	return plan, nil
}

func (s *Service) GetPlan(ctx context.Context, id string) (*Plan, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("%w: plan id is required", ErrInvalid)
	}
	return s.repo.GetReleaseV2Plan(ctx, id)
}

func (s *Service) CreateRun(ctx context.Context, planID, idempotencyKey, requestedBy string) (*Run, error) {
	plan, err := s.GetPlan(ctx, planID)
	if err != nil {
		return nil, err
	}
	if err := plan.ValidateForRun(s.now()); err != nil {
		return nil, err
	}
	run, targets, err := RunFromPlan(plan, idempotencyKey, requestedBy)
	if err != nil {
		return nil, err
	}
	created, err := s.repo.CreateRunFromPlan(ctx, run, targets)
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (s *Service) GetRun(ctx context.Context, id string) (*Run, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("%w: run id is required", ErrInvalid)
	}
	return s.repo.GetRun(ctx, id)
}

func (s *Service) GetRunBundle(ctx context.Context, id string, eventAfter int64, eventLimit int) (*RunBundle, error) {
	run, err := s.GetRun(ctx, id)
	if err != nil {
		return nil, err
	}
	targets, err := s.repo.ListTargets(ctx, id)
	if err != nil {
		return nil, err
	}
	artifacts, err := s.repo.ListArtifacts(ctx, id)
	if err != nil {
		return nil, err
	}
	publications, err := s.repo.ListPublications(ctx, id)
	if err != nil {
		return nil, err
	}
	events, err := s.repo.ListEvents(ctx, id, eventAfter, eventLimit)
	if err != nil {
		return nil, err
	}
	return &RunBundle{Run: run, Targets: targets, Artifacts: artifacts, Publications: publications, Events: events}, nil
}

func (s *Service) ListRuns(ctx context.Context, projectID string, limit int) ([]Run, error) {
	return s.repo.ListRuns(ctx, strings.TrimSpace(projectID), limit)
}

func (s *Service) ListExecutors(ctx context.Context) ([]Executor, error) {
	return s.repo.ListExecutors(ctx)
}

func (s *Service) CancelRun(ctx context.Context, id, actor, reason string) (*Run, error) {
	run, err := s.GetRun(ctx, id)
	if err != nil {
		return nil, err
	}
	if !CanTransition(run.Status, RunCancelled) {
		return nil, fmt.Errorf("%w: cannot cancel run in %s", ErrState, run.Status)
	}
	ok, err := s.repo.UpdateRunStatus(ctx, id, run.Status, RunCancelled, "cancelled", firstNonEmpty(reason, "cancelled by user"))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: run changed while cancelling", ErrState)
	}
	_ = s.repo.AppendEvent(ctx, Event{RunID: id, Actor: firstNonEmpty(actor, "admin"), Kind: "run.cancelled", Summary: firstNonEmpty(reason, "release run cancelled")})
	return s.GetRun(ctx, id)
}

func (s *Service) Transition(ctx context.Context, id string, from, to RunStatus, actor, summary string) (bool, error) {
	if !CanTransition(from, to) {
		return false, fmt.Errorf("%w: %s -> %s", ErrState, from, to)
	}
	ok, err := s.repo.UpdateRunStatus(ctx, id, from, to, "", "")
	if err != nil {
		return false, err
	}
	if ok {
		_ = s.repo.AppendEvent(ctx, Event{RunID: id, Actor: actor, Kind: "run.status", Summary: summary, Detail: map[string]any{"from": from, "to": to}})
	}
	return ok, nil
}

func IsTerminal(err error) bool {
	return errors.Is(err, ErrState) || errors.Is(err, ErrInvalid)
}
