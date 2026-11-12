// Package releasesvc 的制品清理：删除失败/取消运行留下的对象和无主对象。
package releasesvc

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"smartpipe/backend/internal/releasev2"
)

// ArtifactStore 是清理器需要的存储能力（LocalStore 与 S3Store 都实现）。
type ArtifactStore interface {
	List(ctx context.Context, prefix string) ([]releasev2.StoredObject, error)
	Delete(ctx context.Context, key string) error
}

// RunStatusSource 批量查询运行状态，避免逐个运行回表。
type RunStatusSource interface {
	ReleaseV2RunRetentions(ctx context.Context, ids []string) (map[string]releasev2.RunRetention, error)
}

// ArtifactJanitor 周期清理制品：
//   - 运行已 failed/cancelled：制品不可能再发布，直接删除；
//   - 对象前缀里的运行在库里不存在（删除运行、早期直传未登记的孤儿）：超过宽限期后删除；
//   - 其它状态（queued/building/built/published…）一律保留。
type ArtifactJanitor struct {
	Objects  ArtifactStore
	Runs     RunStatusSource
	Prefix   string
	Grace     time.Duration
	Retention time.Duration
	Interval  time.Duration
	Now      func() time.Time
	Logf     func(string, ...any)
}

// Run 按间隔执行 Sweep，直到 ctx 取消。
func (j *ArtifactJanitor) Run(ctx context.Context) {
	interval := j.Interval
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	logf := j.logf()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if n, err := j.Sweep(ctx); err != nil {
			logf("artifact gc sweep failed: %v", err)
		} else if n > 0 {
			logf("artifact gc removed %d objects", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Sweep 执行一次清理，返回删除的对象数。
func (j *ArtifactJanitor) Sweep(ctx context.Context) (int, error) {
	if j.Objects == nil || j.Runs == nil {
		return 0, fmt.Errorf("artifact janitor: objects and runs are required")
	}
	prefix := strings.TrimSpace(j.Prefix)
	if prefix == "" {
		prefix = "releases"
	}
	prefix = strings.TrimSuffix(prefix, "/")
	objects, err := j.Objects.List(ctx, prefix+"/")
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0, len(objects))
	seen := map[string]bool{}
	for _, obj := range objects {
		runID := runIDFromObjectKey(obj.Key)
		if runID == "" || seen[runID] {
			continue
		}
		seen[runID] = true
		ids = append(ids, runID)
	}
	statuses, err := j.Runs.ReleaseV2RunRetentions(ctx, ids)
	if err != nil {
		return 0, err
	}
	latestPublished := latestPublishedRuns(statuses)
	now := time.Now
	if j.Now != nil {
		now = j.Now
	}
	grace := j.Grace
	if grace < 0 {
		grace = 0
	}
	logf := j.logf()
	deleted := 0
	for _, obj := range objects {
		runID := runIDFromObjectKey(obj.Key)
		if runID == "" {
			continue
		}
		info, known := statuses[runID]
		reason := ""
		switch {
		case !known:
			if !obj.ModTime.IsZero() && now().Sub(obj.ModTime) < grace {
				continue
			}
			reason = "orphan"
		case info.Status == releasev2.RunFailed || info.Status == releasev2.RunCancelled:
			reason = string(info.Status)
		case info.Status == releasev2.RunBuilt || info.Status == releasev2.RunPublished:
			if j.Retention <= 0 {
				continue
			}
			ref := info.UpdatedAt
			if info.FinishedAt != nil {
				ref = *info.FinishedAt
			}
			if ref.IsZero() || now().Sub(ref) < j.Retention {
				continue
			}
			// 每个项目/渠道保留最新的已发布运行，避免删掉回滚/校验所需的那一份。
			if info.Status == releasev2.RunPublished && latestPublished[runKey(info)] == runID {
				continue
			}
			reason = "retention"
		default:
			continue
		}
		if err := j.Objects.Delete(ctx, obj.Key); err != nil {
			logf("artifact gc delete %s failed: %v", obj.Key, err)
			continue
		}
		deleted++
		logf("artifact gc deleted %s (run %s, reason %s)", obj.Key, runID, reason)
	}
	return deleted, nil
}

func (j *ArtifactJanitor) logf() func(string, ...any) {
	if j.Logf != nil {
		return j.Logf
	}
	return log.Printf
}

// runKey 用项目 + 渠道定位一个发布线。
func runKey(info releasev2.RunRetention) string {
	return info.ProjectID + "/" + info.Channel
}

// latestPublishedRuns 返回每条发布线里最新的已发布运行 ID。
func latestPublishedRuns(statuses map[string]releasev2.RunRetention) map[string]string {
	out := map[string]string{}
	latest := map[string]time.Time{}
	for id, info := range statuses {
		if info.Status != releasev2.RunPublished {
			continue
		}
		ref := info.UpdatedAt
		if info.FinishedAt != nil {
			ref = *info.FinishedAt
		}
		key := runKey(info)
		if cur, ok := latest[key]; !ok || ref.After(cur) {
			latest[key] = ref
			out[key] = id
		}
	}
	return out
}

// runIDFromObjectKey 从 releases/<runID>/... 里取出运行 ID；不符合约定的对象不清理。
func runIDFromObjectKey(key string) string {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(key), "/"), "/")
	if len(parts) < 3 || parts[0] != "releases" {
		return ""
	}
	if !strings.HasPrefix(parts[1], "rrun_") {
		return ""
	}
	return parts[1]
}