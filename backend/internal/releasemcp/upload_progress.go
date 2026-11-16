package releasemcp

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"smartpipe/backend/internal/releasev2"
	"smartpipe/backend/internal/store"
)

// uploadProgressInterval 控制进度事件的写入频率。上传一个 1 GiB 的制品时
// 每 5 秒一条事件足够让详情页看起来是连续的，又不会把事件表写爆。
const uploadProgressInterval = 5 * time.Second

// uploadFile 描述一个正在上传到远端的制品。
type uploadFile struct {
	Name      string
	Component string
	Slot      string
	OS        string
	Arch      string
	Size      int64
}

// uploadProgress 把长时间上传的进度写进运行事件流，供发版详情页实时展示。
// 事件只用于观测：写失败会被忽略，绝不打断发布本身。
type uploadProgress struct {
	store  *store.Store
	runID  string
	actor  string
	prefix string
	target string
	url    string

	mu        sync.Mutex
	files     int
	index     int
	uploaded  int
	skipped   int
	current   uploadFile
	startedAt time.Time
	lastAt    time.Time
	lastBytes int64
}

func newUploadProgress(st *store.Store, runID, actor, prefix, target, url string, files int) *uploadProgress {
	return &uploadProgress{
		store: st, runID: runID, actor: actor, prefix: prefix,
		target: target, url: url, files: files, startedAt: time.Now(),
	}
}

func (p *uploadProgress) emit(ctx context.Context, suffix, summary string, detail map[string]any) {
	if p == nil || p.store == nil {
		return
	}
	if detail == nil {
		detail = map[string]any{}
	}
	detail["service"] = p.target
	if p.url != "" {
		detail["url"] = p.url
	}
	_ = p.store.AppendEvent(ctx, releasev2.Event{
		RunID: p.runID, Actor: p.actor, Kind: p.prefix + "." + suffix,
		TargetID: p.target, Summary: summary, Detail: detail,
	})
}

// counters 需要在持有 p.mu 时调用。
func (p *uploadProgress) counters() map[string]any {
	return map[string]any{
		"file_index": p.index, "file_count": p.files,
		"uploaded": p.uploaded, "skipped": p.skipped,
	}
}

func fileDetail(f uploadFile) map[string]any {
	detail := map[string]any{"name": f.Name, "bytes_total": f.Size}
	if f.Component != "" {
		detail["component"] = f.Component
	}
	if f.Slot != "" {
		detail["slot"] = f.Slot
	}
	if f.OS != "" {
		detail["os"] = f.OS
	}
	if f.Arch != "" {
		detail["arch"] = f.Arch
	}
	return detail
}

// begin 在每个服务真正开始上传前调用，让界面立刻知道有几台服务在跑。
func (p *uploadProgress) begin(ctx context.Context) {
	if p == nil {
		return
	}
	p.mu.Lock()
	detail := p.counters()
	p.mu.Unlock()
	detail["phase"] = "started"
	p.emit(ctx, "service_started", fmt.Sprintf("%s 开始导入，共 %d 个制品", p.target, p.files), detail)
}

// startFile 标记一个制品开始上传，并返回一个能按间隔上报字节数的 reader。
func (p *uploadProgress) startFile(ctx context.Context, f uploadFile, body io.Reader) io.Reader {
	if p == nil {
		return body
	}
	p.mu.Lock()
	p.index++
	p.current = f
	p.lastAt = time.Now()
	p.lastBytes = 0
	detail := p.counters()
	for key, value := range fileDetail(f) {
		detail[key] = value
	}
	index := p.index
	p.mu.Unlock()
	detail["phase"] = "started"
	p.emit(ctx, "progress", fmt.Sprintf("%s 上传 %d/%d：%s（%s）", p.target, index, p.files, f.Name, formatBytes(f.Size)), detail)
	if body == nil {
		return nil
	}
	return &uploadProgressReader{reader: body, report: func(done int64) { p.reportBytes(ctx, done) }}
}

// reportBytes 由 reader 在每次读到数据后调用，事件按 uploadProgressInterval 节流。
func (p *uploadProgress) reportBytes(ctx context.Context, done int64) {
	p.mu.Lock()
	now := time.Now()
	elapsed := now.Sub(p.lastAt)
	if elapsed < uploadProgressInterval {
		p.mu.Unlock()
		return
	}
	delta := done - p.lastBytes
	rate := 0.0
	if seconds := elapsed.Seconds(); seconds > 0 && delta > 0 {
		rate = float64(delta) / seconds
	}
	p.lastAt, p.lastBytes = now, done
	detail := p.counters()
	f := p.current
	p.mu.Unlock()
	for key, value := range fileDetail(f) {
		detail[key] = value
	}
	detail["phase"] = "uploading"
	detail["bytes_done"] = done
	detail["bytes_per_second"] = rate
	p.emit(ctx, "progress", fmt.Sprintf("%s 上传 %s：%d%%（%s/s）", p.target, f.Name, filePercent(done, f.Size), formatBytes(int64(rate))), detail)
}

// finishFile 在一个制品上传成功后调用。
func (p *uploadProgress) finishFile(ctx context.Context, f uploadFile, elapsed time.Duration) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.uploaded++
	index := p.index
	detail := p.counters()
	p.mu.Unlock()
	for key, value := range fileDetail(f) {
		detail[key] = value
	}
	detail["phase"] = "done"
	detail["bytes_done"] = f.Size
	detail["elapsed_seconds"] = int(elapsed.Seconds())
	p.emit(ctx, "progress", fmt.Sprintf("%s 完成 %d/%d：%s（用时 %s）", p.target, index, p.files, f.Name, formatElapsed(elapsed)), detail)
}

// skipFile 在远端已存在相同摘要、直接跳过时调用。
func (p *uploadProgress) skipFile(ctx context.Context, f uploadFile) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.index++
	p.skipped++
	detail := p.counters()
	p.mu.Unlock()
	for key, value := range fileDetail(f) {
		detail[key] = value
	}
	detail["phase"] = "skipped"
	p.emit(ctx, "progress", fmt.Sprintf("%s 跳过 %d/%d：%s（远端已存在相同摘要）", p.target, p.index, p.files, f.Name), detail)
}

func (p *uploadProgress) finished(ctx context.Context) {
	if p == nil {
		return
	}
	p.mu.Lock()
	detail := p.counters()
	elapsed := time.Since(p.startedAt)
	p.mu.Unlock()
	detail["phase"] = "done"
	detail["elapsed_seconds"] = int(elapsed.Seconds())
	p.emit(ctx, "service_done", fmt.Sprintf("%s 导入完成：上传 %d，跳过 %d，用时 %s", p.target, detail["uploaded"], detail["skipped"], formatElapsed(elapsed)), detail)
}

func (p *uploadProgress) failed(ctx context.Context, err error) {
	if p == nil {
		return
	}
	p.mu.Lock()
	detail := p.counters()
	f := p.current
	elapsed := time.Since(p.startedAt)
	p.mu.Unlock()
	for key, value := range fileDetail(f) {
		detail[key] = value
	}
	detail["phase"] = "failed"
	detail["error"] = err.Error()
	detail["elapsed_seconds"] = int(elapsed.Seconds())
	p.emit(ctx, "service_failed", fmt.Sprintf("%s 导入失败：%v", p.target, err), detail)
}

type uploadProgressReader struct {
	reader io.Reader
	done   int64
	report func(int64)
}

func (r *uploadProgressReader) Read(buf []byte) (int, error) {
	n, err := r.reader.Read(buf)
	if n > 0 {
		r.done += int64(n)
		r.report(r.done)
	}
	return n, err
}

func filePercent(done, total int64) int {
	if total <= 0 {
		return 0
	}
	percent := int(done * 100 / total)
	if percent > 100 {
		percent = 100
	}
	if percent < 0 {
		percent = 0
	}
	return percent
}

func formatBytes(size int64) string {
	switch {
	case size >= 1024*1024*1024:
		return fmt.Sprintf("%.2f GiB", float64(size)/(1024*1024*1024))
	case size >= 1024*1024:
		return fmt.Sprintf("%.1f MiB", float64(size)/(1024*1024))
	case size >= 1024:
		return fmt.Sprintf("%.1f KiB", float64(size)/1024)
	default:
		return fmt.Sprintf("%d B", size)
	}
}

func formatElapsed(elapsed time.Duration) string {
	seconds := int(elapsed.Round(time.Second).Seconds())
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	seconds %= 60
	if minutes < 60 {
		return fmt.Sprintf("%dm%02ds", minutes, seconds)
	}
	return fmt.Sprintf("%dh%02dm%02ds", minutes/60, minutes%60, seconds)
}
