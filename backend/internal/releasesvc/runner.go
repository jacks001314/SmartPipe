// Package releasesvc contains the durable worker loop for release v2 runs.
package releasesvc

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"smartpipe/backend/internal/releasev2"
)

type Store interface {
	ListActiveReleaseV2Runs(context.Context, int) ([]releasev2.Run, error)
	ListTargets(context.Context, string) ([]releasev2.Target, error)
	ListExecutors(context.Context) ([]releasev2.Executor, error)
	UpdateRunStatus(context.Context, string, releasev2.RunStatus, releasev2.RunStatus, string, string) (bool, error)
	AppendEvent(context.Context, releasev2.Event) error
	RequeueExpiredLeases(context.Context) (int, error)
}

type Runner struct {
	Store    Store
	Interval time.Duration
	Logf     func(string, ...any)
	// MaxActive 限制同时处于 preparing/building 的运行数；0 表示不限制。
	MaxActive int
	held      map[string]bool
}

func (r *Runner) Run(ctx context.Context) {
	interval := r.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	logf := r.Logf
	if logf == nil {
		logf = log.Printf
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := r.Tick(ctx); err != nil {
			logf("release-service tick failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runner) Tick(ctx context.Context) error {
	if r.Store == nil {
		return fmt.Errorf("release service: store is required")
	}
	if n, err := r.Store.RequeueExpiredLeases(ctx); err != nil {
		return err
	} else if n > 0 {
		// Targets requeued here will be picked up by another executor attempt.
		log.Printf("requeued %d expired release leases", n)
	}
	runs, err := r.Store.ListActiveReleaseV2Runs(ctx, 100)
	if err != nil {
		return err
	}
	var first error
	active := countActiveRuns(runs)
	for i := range runs {
		run := &runs[i]
		if run.Status == releasev2.RunQueued && r.MaxActive > 0 && active >= r.MaxActive {
			r.noteHeld(ctx, run, active)
			continue
		}
		if r.held != nil {
			delete(r.held, run.ID)
		}
		if err := r.advance(ctx, run); err != nil && first == nil {
			first = err
		}
		active = countActiveRuns(runs)
	}
	return first
}

// countActiveRuns 统计占着执行槽位的运行：staged/publishing 等 Agent 或审批的运行不算。
func countActiveRuns(runs []releasev2.Run) int {
	active := 0
	for _, run := range runs {
		switch run.Status {
		case releasev2.RunPreparing, releasev2.RunBuilding, releasev2.RunVerifying, releasev2.RunRecovering:
			active++
		}
	}
	return active
}

// noteHeld 对同一个运行只记一次排队事件，避免每个 tick 刷屏。
func (r *Runner) noteHeld(ctx context.Context, run *releasev2.Run, active int) {
	if r.held == nil {
		r.held = map[string]bool{}
	}
	if r.held[run.ID] {
		return
	}
	r.held[run.ID] = true
	_ = r.Store.AppendEvent(ctx, releasev2.Event{
		RunID: run.ID, Actor: "release-service", Kind: "run.waiting",
		Summary: "waiting for an execution slot",
		Detail:  map[string]any{"active": active, "max_active": r.MaxActive},
	})
}

func (r *Runner) advance(ctx context.Context, run *releasev2.Run) error {
	switch run.Status {
	case releasev2.RunQueued:
		return r.move(ctx, run, releasev2.RunPreparing, "release-service", "release run accepted")
	case releasev2.RunPreparing:
		targets, err := r.Store.ListTargets(ctx, run.ID)
		if err != nil {
			return err
		}
		if len(targets) == 0 {
			return r.block(ctx, run, "targets_missing", "release run has no targets")
		}
		executors, err := r.Store.ListExecutors(ctx)
		if err != nil {
			return err
		}
		online := map[string]bool{}
		for _, executor := range executors {
			if strings.EqualFold(executor.Status, "online") {
				online[executor.ID] = true
			}
		}
		for _, target := range targets {
			if target.ExecutorID == "" {
				return r.block(ctx, run, "executor_required", "target "+target.TargetID+" has no executor")
			}
			if !online[target.ExecutorID] {
				return r.block(ctx, run, "executor_offline", "executor "+target.ExecutorID+" is offline")
			}
		}
		return r.move(ctx, run, releasev2.RunBuilding, "release-service", "dispatch targets to release executors")
	case releasev2.RunBuilding, releasev2.RunVerifying:
		// verifying 是旧版本的状态：新运行在 building 结束时直接进入制品就绪
		// 阶段。历史运行可能停在这里，按同一套判定收口，避免失败目标永久占着
		// 执行槽位却始终等不到 Agent。
		targets, err := r.Store.ListTargets(ctx, run.ID)
		if err != nil {
			return err
		}
		ready := 0
		for _, target := range targets {
			if target.Status == releasev2.TargetFailed {
				return r.attention(ctx, run, "target_failed", "target "+target.TargetID+": "+firstNonEmpty(target.ErrorMessage, target.ErrorCode))
			}
			if target.Status == releasev2.TargetReady {
				ready++
			}
		}
		if ready != len(targets) || strings.TrimSpace(run.ManifestStorageKey) == "" {
			return nil
		}
		return r.artifactsReady(ctx, run)
	case releasev2.RunArtifactsReady:
		// 制品清单已合并，后续升级包与渠道推进由 Release Agent 通过 release-mcp 完成。
		return nil
	case releasev2.RunRecovering:
		return r.move(ctx, run, releasev2.RunPreparing, "release-service", "agent requested recovery")
	case releasev2.RunNeedsAttention:
		// 显式停靠的运行（例如自动接管预算用尽）不能被“目标都 ready”的
		// 事实收回 artifacts_ready：attention worker 下一轮又会把它停靠
		// 回来，两边来回搬状态会让事件面板无限刷屏。
		if run.ErrorCode == releasev2.ErrorRecoveryExhausted {
			return nil
		}
		// Agent 会话结束时运行会被交回 needs_attention，但重试可能已经成功：
		// 所有必需目标 ready、run 清单已合并时没有可重试的失败目标，继续等
		// Agent 会让运行永远停住。这里按事实状态收口，不覆盖仍然失败的目标。
		targets, err := r.Store.ListTargets(ctx, run.ID)
		if err != nil {
			return err
		}
		if len(targets) == 0 {
			return nil
		}
		for _, target := range targets {
			if target.Status == releasev2.TargetFailed || (target.Required && target.Status != releasev2.TargetReady) {
				return nil
			}
		}
		if strings.TrimSpace(run.ManifestStorageKey) == "" {
			return nil
		}
		return r.artifactsReady(ctx, run)
	default:
		return nil
	}
}

func (r *Runner) artifactsReady(ctx context.Context, run *releasev2.Run) error {
	if run.Mode == releasev2.ModeBuildOnly {
		return r.move(ctx, run, releasev2.RunBuilt, "release-service", "build-only release completed")
	}
	return r.move(ctx, run, releasev2.RunArtifactsReady, "release-service", "all targets ready and run manifest assembled")
}

func (r *Runner) move(ctx context.Context, run *releasev2.Run, to releasev2.RunStatus, actor, summary string) error {
	ok, err := r.Store.UpdateRunStatus(ctx, run.ID, run.Status, to, "", "")
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	_ = r.Store.AppendEvent(ctx, releasev2.Event{
		RunID: run.ID, Actor: actor, Kind: "run.status", Summary: summary,
		Detail: map[string]any{"from": run.Status, "to": to},
	})
	run.Status = to
	return nil
}

func (r *Runner) block(ctx context.Context, run *releasev2.Run, code, message string) error {
	ok, err := r.Store.UpdateRunStatus(ctx, run.ID, run.Status, releasev2.RunBlocked, code, message)
	if err != nil {
		return err
	}
	if ok {
		_ = r.Store.AppendEvent(ctx, releasev2.Event{
			RunID: run.ID, Actor: "release-service", Kind: "run.blocked", Summary: message,
			Detail: map[string]any{"code": code, "from": run.Status},
		})
	}
	return nil
}

func (r *Runner) fail(ctx context.Context, run *releasev2.Run, code, message string) error {
	ok, err := r.Store.UpdateRunStatus(ctx, run.ID, run.Status, releasev2.RunFailed, code, message)
	if err != nil {
		return err
	}
	if ok {
		_ = r.Store.AppendEvent(ctx, releasev2.Event{
			RunID: run.ID, Actor: "release-service", Kind: "run.failed", Summary: message,
			Detail: map[string]any{"code": code, "from": run.Status},
		})
	}
	return nil
}

func (r *Runner) attention(ctx context.Context, run *releasev2.Run, code, message string) error {
	ok, err := r.Store.UpdateRunStatus(ctx, run.ID, run.Status, releasev2.RunNeedsAttention, code, message)
	if err != nil {
		return err
	}
	if ok {
		_ = r.Store.AppendEvent(ctx, releasev2.Event{
			RunID: run.ID, Actor: "release-service", Kind: "run.needs_attention", Summary: message,
			Detail: map[string]any{"code": code, "from": run.Status},
		})
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
