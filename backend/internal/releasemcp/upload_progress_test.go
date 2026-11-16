package releasemcp

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"
)

func TestUploadProgressReaderCountsBytes(t *testing.T) {
	var seen []int64
	reader := &uploadProgressReader{reader: bytes.NewReader([]byte("0123456789")), report: func(done int64) { seen = append(seen, done) }}
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatal(err)
	}
	if len(seen) == 0 || seen[len(seen)-1] != 10 {
		t.Fatalf("reported bytes = %v", seen)
	}
}

func TestUploadProgressCountersTrackFiles(t *testing.T) {
	ctx := context.Background()
	// store 为 nil 时 emit 直接跳过，这里只验证计数和索引推进。
	progress := newUploadProgress(nil, "run", "agent", "update_import", "cdn", "", 3)
	progress.begin(ctx)
	file := uploadFile{Name: "app.zip", Component: "product", Size: 10}
	if _, err := io.ReadAll(progress.startFile(ctx, file, bytes.NewReader([]byte("0123456789")))); err != nil {
		t.Fatal(err)
	}
	progress.finishFile(ctx, file, 900*time.Millisecond)
	progress.skipFile(ctx, uploadFile{Name: "cdn.tgz", Component: "product", Size: 5})

	progress.mu.Lock()
	detail := progress.counters()
	progress.mu.Unlock()
	if detail["file_index"] != 2 || detail["file_count"] != 3 {
		t.Fatalf("file progress = %v", detail)
	}
	if detail["uploaded"] != 1 || detail["skipped"] != 1 {
		t.Fatalf("upload counters = %v", detail)
	}
}

func TestUploadProgressFormatting(t *testing.T) {
	if got := filePercent(512, 1024); got != 50 {
		t.Fatalf("filePercent = %d, want 50", got)
	}
	if got := filePercent(5, 0); got != 0 {
		t.Fatalf("filePercent without total = %d, want 0", got)
	}
	if got := formatBytes(1536); got != "1.5 KiB" {
		t.Fatalf("formatBytes = %q", got)
	}
	if got := formatElapsed(90 * time.Second); got != "1m30s" {
		t.Fatalf("formatElapsed = %q", got)
	}
}
