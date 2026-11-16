package adminapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"smartpipe/backend/internal/releaseagent"
	"smartpipe/backend/internal/releasev2"
)

// StartReleaseV2AttentionWorker 周期性把 needs_attention 的运行重新交给发布
// Agent。Launcher 自带同运行去重，这里再加冷却时间与每轮上限，避免失败时空转。
func (a *API) StartReleaseV2AttentionWorker(ctx context.Context, interval time.Duration, maxAttempts int) {
	if a.releaseAgent == nil || !a.releaseAgent.Enabled() {
		return
	}
	if interval <= 0 {
		interval = 20 * time.Second
	}
	go a.attentionLoop(ctx, interval, maxAttempts)
}

func (a *API) attentionLoop(ctx context.Context, interval time.Duration, maxAttempts int) {
	cooldown := interval
	if cooldown < 30*time.Second {
		cooldown = 30 * time.Second
	}
	last := map[string]time.Time{}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.resumeAttentionRuns(ctx, maxAttempts, cooldown, last)
			if len(last) > 500 {
				cutoff := time.Now().Add(-4 * cooldown)
				for id, at := range last {
					if at.Before(cutoff) {
						delete(last, id)
					}
				}
			}
		}
	}
}

func (a *API) resumeAttentionRuns(ctx context.Context, maxAttempts int, cooldown time.Duration, last map[string]time.Time) {
	runs, err := a.store.ListAttentionReleaseV2Runs(ctx, 200)
	if err != nil {
		log.Printf("发布 Agent attention 扫描失败: %v", err)
		return
	}
	for i := range runs {
		run := &runs[i]
		if !attentionEligible(run) || a.releaseAgent.Running(run.ID) {
			continue
		}
		// 非 needs_attention 的运行只有在"停住"一段时间后才接管：上一次会话
		// 可能刚写完状态还没退出，立刻重开会话会和它抢同一份运行。
		if run.Status != releasev2.RunNeedsAttention && time.Since(run.UpdatedAt) < cooldown {
			continue
		}
		if maxAttempts > 0 && run.RecoveryAttempts >= maxAttempts {
			// 重试预算用尽还停在中间态时必须显式落到 needs_attention：
			// 否则 runner 会把运行收回 artifacts_ready，表面看像正常，
			// 实际再也没有任何东西会继续灌库和推渠道。
			if run.Status != releasev2.RunNeedsAttention {
				if moved, err := a.store.MarkReleaseV2RunNeedsAttention(ctx, run.ID,
					fmt.Sprintf("自动接管次数已用尽（%d/%d）", run.RecoveryAttempts, maxAttempts)); err == nil && moved {
					log.Printf("发布运行 %s 自动接管次数用尽，已标记 needs_attention", run.ID)
				}
			}
			continue
		}
		if at, seen := last[run.ID]; seen && time.Since(at) < cooldown {
			continue
		}
		last[run.ID] = time.Now()

		// 先占一次重试预算再启动会话。放到启动之后会失效：会话一启动就会把
		// 运行推进出新状态，BumpReleaseV2RecoveryAttempts 的状态条件不满足，
		// 静默返回 0，重试上限永远达不到。真实事故：一个被版本冲突拦下的
		// 运行在 3 小时里空转了 217 个会话。
		attempts, bumpErr := a.store.BumpReleaseV2RecoveryAttempts(ctx, run.ID, maxAttempts)
		if bumpErr != nil {
			log.Printf("发布 Agent attention 记录尝试次数 %s 失败: %v", run.ID, bumpErr)
			continue
		}
		if attempts == 0 {
			continue
		}

		bundle, err := a.releaseV2.GetRunBundle(ctx, run.ID, 0, 1)
		if err != nil {
			log.Printf("发布 Agent attention 读取运行 %s 失败: %v", run.ID, err)
			continue
		}
		session, err := a.releaseAgent.Launch(ctx, bundle, a.releaseAgentOptions(ctx, run.ProjectID, releaseagent.Options{Actor: "system:attention"}))
		if err != nil {
			if errors.Is(err, releaseagent.ErrAlreadyRunning) || errors.Is(err, releaseagent.ErrLimit) {
				continue
			}
			log.Printf("发布 Agent 自动接管 %s 失败: %v", run.ID, err)
			_ = a.store.AppendEvent(ctx, releasev2.Event{
				RunID: run.ID, Actor: "system:attention", Kind: "agent.attention_failed",
				Summary: "自动接管 needs_attention 运行失败", Detail: map[string]any{"error": err.Error()},
			})
			continue
		}
		_ = a.store.AppendEvent(ctx, releasev2.Event{
			RunID: run.ID, Actor: "system:attention", Kind: "agent.attention_resume",
			Summary: "运行已自动交回发布 Agent（" + string(run.Status) + "）",
			Detail:  map[string]any{"session_id": session.ID, "attempt": attempts, "max_attempts": maxAttempts, "from": string(run.Status)},
		})
	}
}

// attentionEligible 判断一条运行是否需要交回发布 Agent。
//
// needs_attention 永远接管（构建失败、会话异常退出都会落到这里）。其余状态
// 只在 publish 模式下接管：runner 会把"目标其实都已 ready"的 needs_attention
// 收口回 artifacts_ready，如果此时 Agent 会话已经异常退出，就没有任何东西会
// 继续灌库和推进渠道，运行会永久停在中间态。build_only 运行停在
// artifacts_ready/built 是正常收口，不能接管。
//
// partially_published 也必须接管：渠道指针已经推进、GitHub 还没有收口，此时
// 会话中断会让运行永久停在"半发布"，升级服务已对外、GitHub 却没有发行版。
// release_publish 从该状态续跑是幂等的（finishPublish 会重做 GitHub 并再回读），
// 因此用同一份 recovery 预算重新交给 Agent 是对的方向。
func attentionEligible(run *releasev2.Run) bool {
	if run == nil {
		return false
	}
	if run.Status == releasev2.RunNeedsAttention {
		return true
	}
	if run.Mode != releasev2.ModePublish {
		return false
	}
	switch run.Status {
	case releasev2.RunVerifying, releasev2.RunArtifactsReady, releasev2.RunStaged,
		releasev2.RunPublishing, releasev2.RunRecovering, releasev2.RunPartiallyPublished:
		return true
	}
	return false
}
