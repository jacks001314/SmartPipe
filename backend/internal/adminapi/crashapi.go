package adminapi

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/kataras/iris/v12"
)

func (a *API) registerCrashes(p iris.Party) {
	p.Get("/crashes", a.listCrashes)
	p.Get("/crashes/retention", a.crashRetention)
	p.Post("/crashes/retention/run", a.runCrashRetention)
	p.Get("/crashes/{id}", a.getCrash)
}

func (a *API) listCrashes(c iris.Context) {
	items, err := a.store.ListCrashReports(c.Request().Context(), intQuery(c, "limit", 100))
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) getCrash(c iris.Context) {
	id, err := strconv.ParseInt(c.Params().Get("id"), 10, 64)
	if err != nil || id <= 0 {
		a.badRequest(c, "非法 id")
		return
	}
	item, err := a.store.GetCrashReport(c.Request().Context(), id)
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: item})
}

func (a *API) retentionDays() (int, int) {
	bundles := a.crashRetentionDays
	if bundles <= 0 {
		bundles = 30
	}
	rejected := a.rejectedRetentionDays
	if rejected <= 0 {
		rejected = 7
	}
	return bundles, rejected
}

// PurgeCrashes 按保留策略清理：超期包的正文清空，超期的拒绝记录删除。
func (a *API) PurgeCrashes(ctx context.Context, now time.Time) (map[string]any, error) {
	bundleDays, rejectedDays := a.retentionDays()
	cleared, err := a.store.PurgeCrashBundles(ctx, now.AddDate(0, 0, -bundleDays))
	if err != nil {
		return nil, err
	}
	deleted, err := a.store.PurgeRejectedCrashes(ctx, now.AddDate(0, 0, -rejectedDays))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"bundle_days": bundleDays, "rejected_days": rejectedDays,
		"bundles_cleared": cleared, "rejected_deleted": deleted,
		"ran_at": now.UTC().Format(time.RFC3339),
	}, nil
}

func (a *API) crashRetention(c iris.Context) {
	rows, bytes, err := a.store.CrashStorageStats(c.Request().Context())
	if err != nil {
		a.fail(c, err)
		return
	}
	bundleDays, rejectedDays := a.retentionDays()
	_ = c.JSON(envelope{Data: iris.Map{
		"rows": rows, "bundle_bytes": bytes,
		"bundle_days": bundleDays, "rejected_days": rejectedDays,
	}})
}

func (a *API) runCrashRetention(c iris.Context) {
	res, err := a.PurgeCrashes(c.Request().Context(), time.Now())
	if err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "crash.retention", "crash_reports", "all", nil, "ok", res)
	_ = c.JSON(envelope{Data: res})
}

// StartRetentionWorker 每 interval 执行一次保留策略；interval<=0 时默认 6 小时。
func (a *API) StartRetentionWorker(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				res, err := a.PurgeCrashes(ctx, time.Now())
				if err != nil {
					log.Printf("崩溃保留策略执行失败: %v", err)
					continue
				}
				if res["bundles_cleared"].(int64) > 0 || res["rejected_deleted"].(int64) > 0 {
					log.Printf("崩溃保留策略: 清空 %v 个包, 删除 %v 条拒绝记录", res["bundles_cleared"], res["rejected_deleted"])
				}
			}
		}
	}()
}

var _ = http.StatusOK
