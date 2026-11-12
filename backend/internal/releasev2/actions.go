package releasev2

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// LogChunk is one ordered piece of executor build output.
type LogChunk struct {
	EventID   int64     `json:"event_id"`
	TargetID  string    `json:"target_id"`
	Seq       int       `json:"seq"`
	Chunk     string    `json:"chunk"`
	Actor     string    `json:"actor,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// DispatchBuild moves a queued or previously blocked run into preparing so the
// executor control loop starts leasing its targets. It is idempotent: calling it
// on a run that already progressed returns the current run unchanged.
func (s *Service) DispatchBuild(ctx context.Context, runID, actor string) (*Run, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	switch run.Status {
	case RunQueued, RunBlocked:
		ok, err := s.repo.UpdateRunStatus(ctx, run.ID, run.Status, RunPreparing, "", "")
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%w: run changed while dispatching", ErrState)
		}
		_ = s.repo.AppendEvent(ctx, Event{
			RunID: run.ID, Actor: firstNonEmpty(actor, "agent"), Kind: "build.dispatched",
			Summary: "build dispatched", Detail: map[string]any{"from": run.Status},
		})
		return s.GetRun(ctx, run.ID)
	case RunPreparing, RunBuilding, RunVerifying:
		return run, nil
	default:
		return nil, fmt.Errorf("%w: cannot dispatch build in %s", ErrState, run.Status)
	}
}

// RetryTarget requeues a failed target for another executor attempt.
func (s *Service) RetryTarget(ctx context.Context, runID, targetID, actor, reason string) (*Target, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	switch run.Status {
	case RunPublished, RunCancelled, RunBuilt:
		return nil, fmt.Errorf("%w: run already finished (%s)", ErrState, run.Status)
	}
	target, err := s.repo.GetTarget(ctx, runID, targetID)
	if err != nil {
		return nil, err
	}
	if target.Status != TargetFailed {
		return nil, fmt.Errorf("%w: target %s is %s", ErrState, targetID, target.Status)
	}
	ok, err := s.repo.RetryReleaseV2Target(ctx, runID, targetID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: target changed while retrying", ErrState)
	}
	_ = s.repo.AppendEvent(ctx, Event{
		RunID: runID, Actor: firstNonEmpty(actor, "agent"), Kind: "target.retry_requested", TargetID: targetID,
		Summary: firstNonEmpty(reason, "target requeued after failure"),
	})
	// A blocked or failed run resumes as soon as a retry is requested.
	if run.Status == RunBlocked || run.Status == RunFailed {
		if ok, err := s.repo.UpdateRunStatus(ctx, runID, run.Status, RunPreparing, "", ""); err == nil && ok {
			_ = s.repo.AppendEvent(ctx, Event{RunID: runID, Actor: firstNonEmpty(actor, "agent"), Kind: "run.status",
				Summary: "resumed after target retry", Detail: map[string]any{"from": run.Status, "to": RunPreparing}})
		}
	}
	return s.repo.GetTarget(ctx, runID, targetID)
}

// SaveNotes updates the release notes of an open run and records their digest.
func (s *Service) SaveNotes(ctx context.Context, runID, notes, actor string) (*Run, error) {
	notes = strings.TrimSpace(notes)
	if notes == "" {
		return nil, fmt.Errorf("%w: notes are empty", ErrInvalid)
	}
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if TerminalRunStatus(run.Status) {
		return nil, fmt.Errorf("%w: run is %s", ErrState, run.Status)
	}
	sha := HashText(notes)
	ok, err := s.repo.UpdateRunNotes(ctx, runID, notes, sha)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: run changed while saving notes", ErrState)
	}
	_ = s.repo.AppendEvent(ctx, Event{RunID: runID, Actor: firstNonEmpty(actor, "agent"), Kind: "run.notes_saved",
		Summary: "release notes saved", Detail: map[string]any{"notes_sha256": sha, "length": len(notes)}})
	return s.GetRun(ctx, runID)
}

// AppendLogChunk stores a chunk of build output from an executor.
func (s *Service) AppendLogChunk(ctx context.Context, runID, targetID, chunk string, seq int, actor string) error {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(targetID) == "" {
		return fmt.Errorf("%w: run_id and target are required", ErrInvalid)
	}
	if chunk == "" {
		return nil
	}
	if len(chunk) > 32<<10 {
		return fmt.Errorf("%w: log chunk exceeds 32KiB", ErrInvalid)
	}
	if _, err := s.repo.GetTarget(ctx, runID, targetID); err != nil {
		return err
	}
	return s.repo.AppendLogChunk(ctx, runID, targetID, chunk, seq, actor)
}

// Logs returns build log chunks ordered by event id.
func (s *Service) Logs(ctx context.Context, runID, targetID string, after int64, limit int) ([]LogChunk, error) {
	events, err := s.repo.ListLogChunks(ctx, runID, targetID, after, limit)
	if err != nil {
		return nil, err
	}
	out := make([]LogChunk, 0, len(events))
	for _, e := range events {
		chunk, _ := e.Detail["chunk"].(string)
		seq := 0
		if v, ok := e.Detail["seq"].(float64); ok {
			seq = int(v)
		}
		out = append(out, LogChunk{EventID: e.ID, TargetID: e.TargetID, Seq: seq, Chunk: chunk, Actor: e.Actor, CreatedAt: e.CreatedAt})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].EventID < out[j].EventID })
	return out, nil
}

// TargetProgress flattens target state for MCP wait loops.
type TargetProgress struct {
	TargetID string       `json:"target_id"`
	Status   TargetStatus `json:"status"`
	Attempt  int          `json:"attempt"`
	Error    string       `json:"error,omitempty"`
}

func (s *Service) Progress(ctx context.Context, runID string) (*Run, []TargetProgress, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	targets, err := s.repo.ListTargets(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	out := make([]TargetProgress, 0, len(targets))
	for _, t := range targets {
		out = append(out, TargetProgress{TargetID: t.TargetID, Status: t.Status, Attempt: t.Attempt,
			Error: firstNonEmpty(t.ErrorCode, t.ErrorMessage)})
	}
	return run, out, nil
}
