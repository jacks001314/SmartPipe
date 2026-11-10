package scheduler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"smartpipe/backend/internal/codexrunner"
)

func outputEvent(method string, params any) codexrunner.Event {
	raw, _ := json.Marshal(params)
	return codexrunner.Event{Method: method, Params: raw, At: time.Now()}
}

func TestOutputCollectorMergesItemsAndFileChanges(t *testing.T) {
	repo := NewMemoryRepo()
	collector := newWorkerOutputCollector(repo, newOutputBlobStore(t.TempDir()), "wk-1", 2)
	collector.handle(outputEvent("turn/started", map[string]any{"turnId": "turn-1"}))
	collector.handle(outputEvent("item/started", map[string]any{"item": map[string]any{
		"id": "msg-1", "type": "agentMessage", "status": "inProgress", "text": "",
	}}))
	collector.handle(outputEvent("item/agentMessage/delta", map[string]any{
		"itemId": "msg-1", "turnId": "turn-1", "delta": "正在检查",
	}))
	collector.handle(outputEvent("item/agentMessage/delta", map[string]any{
		"itemId": "msg-1", "turnId": "turn-1", "delta": "登录流程。",
	}))
	collector.handle(outputEvent("item/commandExecution/outputDelta", map[string]any{
		"itemId": "cmd-1", "turnId": "turn-1", "delta": "PASS\n",
	}))
	collector.handle(outputEvent("item/fileChange/patchUpdated", map[string]any{
		"itemId": "files-1", "turnId": "turn-1",
		"changes": []any{map[string]any{"path": "auth.go", "kind": "update", "diff": "@@ -1 +1,2 @@\n-old\n+new\n+extra\n"}},
	}))
	collector.handle(outputEvent("item/completed", map[string]any{"item": map[string]any{
		"id": "msg-1", "type": "agentMessage", "status": "completed", "text": "正在检查登录流程。",
	}}))
	collector.close("completed")

	items, err := repo.ListWorkerOutputs(context.Background(), "wk-1", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("want 3 merged items, got %d: %#v", len(items), items)
	}
	byID := map[string]WorkerOutput{}
	for _, item := range items {
		byID[item.ItemID] = item
	}
	msg := byID["msg-1"]
	if msg.Content != "正在检查登录流程。" || msg.Status != "completed" || msg.Round != 2 || msg.TurnID != "turn-1" {
		t.Fatalf("unexpected message output: %+v", msg)
	}
	if byID["cmd-1"].Output != "PASS\n" || byID["cmd-1"].Kind != OutputKindCommand {
		t.Fatalf("unexpected command output: %+v", byID["cmd-1"])
	}
	file := byID["files-1"]
	var detail struct {
		Files []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(file.Metadata, &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Files) != 1 || intAny(detail.Files[0]["additions"]) != 2 || intAny(detail.Files[0]["deletions"]) != 1 {
		t.Fatalf("file stats missing: %s", file.Metadata)
	}
}

func TestOutputCollectorExternalizesLargeTerminalOutput(t *testing.T) {
	repo := NewMemoryRepo()
	root := t.TempDir()
	collector := newWorkerOutputCollector(repo, newOutputBlobStore(root), "wk-big", 1)
	collector.handle(outputEvent("turn/started", map[string]any{"turnId": "turn-big"}))
	large := strings.Repeat("\x1b[31m0123456789abcdef\x1b[0m OPENAI_API_KEY=super-secret\n", 2200)
	collector.handle(outputEvent("item/commandExecution/outputDelta", map[string]any{
		"itemId": "cmd-big", "turnId": "turn-big", "delta": large,
	}))
	collector.close("completed")

	items, err := repo.ListWorkerOutputs(context.Background(), "wk-big", 0, 20)
	if err != nil || len(items) != 1 {
		t.Fatalf("outputs=%#v err=%v", items, err)
	}
	item := items[0]
	if !item.Truncated || item.BlobRef == "" {
		t.Fatalf("large output should be externalized: %+v", item)
	}
	if len(item.Output) > outputExternalizeThreshold || !strings.Contains(item.Output, "输出已外置") {
		t.Fatalf("inline event should keep only excerpt, bytes=%d", len(item.Output))
	}
	s := &Scheduler{repo: repo, outputBlobs: newOutputBlobStore(root)}
	page, err := s.WorkerOutputRaw(context.Background(), "wk-big", "cmd-big", 0, outputPageBytes)
	if err != nil {
		t.Fatal(err)
	}
	if page.TotalBytes <= int64(outputExternalizeThreshold) || !strings.Contains(page.Data, "0123456789abcdef") {
		t.Fatalf("raw page should read the original log: total=%d", page.TotalBytes)
	}
	if strings.Contains(page.Data, "\x1b[") || strings.Contains(page.Data, "super-secret") {
		t.Fatal("terminal escapes and obvious secrets must be cleaned before persistence")
	}
	if !strings.Contains(page.Data, "[redacted]") {
		t.Fatal("secret value should retain a redaction marker")
	}
}

func TestWorkerOutputRevisionCursor(t *testing.T) {
	repo := NewMemoryRepo()
	first := WorkerOutput{WorkerID: "wk", ItemID: "a", Kind: OutputKindAgentMessage, Content: "a"}
	second := WorkerOutput{WorkerID: "wk", ItemID: "b", Kind: OutputKindAgentMessage, Content: "b"}
	if err := repo.UpsertWorkerOutput(context.Background(), &first); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertWorkerOutput(context.Background(), &second); err != nil {
		t.Fatal(err)
	}
	// 更新旧 item 会得到更大的 revision，客户端据此原位替换，不插入新行。
	first.Content = "a2"
	if err := repo.UpsertWorkerOutput(context.Background(), &first); err != nil {
		t.Fatal(err)
	}
	items, err := repo.ListWorkerOutputs(context.Background(), "wk", second.Revision, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ItemID != "a" || items[0].Content != "a2" {
		t.Fatalf("cursor should return only the updated item: %#v", items)
	}
}

func TestPauseFlushesPartialWorkerOutputAsCancelled(t *testing.T) {
	h := newHarness(t, Config{})
	started := make(chan struct{}, 1)
	h.s.runner = RunnerFunc(func(ctx context.Context, spec codexrunner.Spec) (*codexrunner.Result, error) {
		spec.OnEvent(outputEvent("turn/started", map[string]any{"turnId": "turn-pause"}))
		spec.OnEvent(outputEvent("item/commandExecution/outputDelta", map[string]any{
			"itemId": "cmd-pause", "turnId": "turn-pause", "delta": "partial output\n",
		}))
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	w := &Worker{ProjectID: "prj-1", Issue: 51, State: StateQueued, ChatID: "group-1"}
	if _, err := h.repo.CreateWorker(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if err := h.s.DispatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not start")
	}
	if _, err := h.s.PauseWorker(context.Background(), w.ID); err != nil {
		t.Fatal(err)
	}
	h.s.Wait()
	items, err := h.repo.ListWorkerOutputs(context.Background(), w.ID, 0, 20)
	if err != nil || len(items) != 1 {
		t.Fatalf("outputs=%#v err=%v", items, err)
	}
	if items[0].Status != "cancelled" || items[0].Output != "partial output\n" {
		t.Fatalf("pause should flush a cancelled item, got %+v", items[0])
	}
}
