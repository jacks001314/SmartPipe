package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"smartpipe/backend/internal/secretbox"
	"smartpipe/backend/internal/store"
	"smartpipe/backend/internal/testdb"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := testdb.DSN(t)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.NewFromBase64(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), dsn, box)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		_ = st.DB().Close()
	})
	_, _ = st.DB().Exec("DELETE FROM scheduler_workers")
	_, _ = st.DB().Exec("DELETE FROM scheduler_links")
	_, _ = st.DB().Exec("DELETE FROM scheduler_chat")
	return st
}

func TestPgRepoWorkerLifecycle(t *testing.T) {
	st := newTestStore(t)
	r := NewPgRepo(st.DB())
	ctx := context.Background()

	w := &Worker{ProjectID: "prj-1", Issue: 1, State: StateQueued, ChatID: "g1"}
	ok, err := r.CreateWorker(ctx, w)
	if err != nil || !ok {
		t.Fatalf("create: ok=%v err=%v", ok, err)
	}
	dup := &Worker{ProjectID: "prj-1", Issue: 1, State: StateQueued}
	if ok, err := r.CreateWorker(ctx, dup); err != nil || ok {
		t.Fatalf("duplicate must be rejected: ok=%v err=%v", ok, err)
	}

	claimed, err := r.ClaimNext(ctx, 1, 1)
	if err != nil || claimed == nil || claimed.ID != w.ID {
		t.Fatalf("claim: %+v err=%v", claimed, err)
	}
	if claimed.State != StatePreparing {
		t.Fatalf("state=%s", claimed.State)
	}
	// 并发上限 1：第二个任务应排队。
	second := &Worker{ProjectID: "prj-1", Issue: 2, State: StateQueued}
	if ok, _ := r.CreateWorker(ctx, second); !ok {
		t.Fatal("create second failed")
	}
	if got, _ := r.ClaimNext(ctx, 1, 1); got != nil {
		t.Fatalf("must respect global limit, claimed %+v", got)
	}

	claimed.State = StateAwaitingReview
	if err := r.UpdateWorker(ctx, claimed); err != nil {
		t.Fatal(err)
	}
	got, _ := r.ClaimNext(ctx, 1, 1)
	if got == nil || got.Issue != 2 {
		t.Fatalf("should claim second after slot free: %+v", got)
	}
}

func TestPgRepoLinksAndChat(t *testing.T) {
	st := newTestStore(t)
	r := NewPgRepo(st.DB())
	ctx := context.Background()
	if err := r.PutLink(ctx, Link{PlatformMessageID: "m1", ProjectID: "p", Issue: 3, ChatID: "g"}); err != nil {
		t.Fatal(err)
	}
	link, err := r.GetLink(ctx, "m1")
	if err != nil || link == nil || link.Issue != 3 {
		t.Fatalf("link: %+v err=%v", link, err)
	}
	for _, b := range []string{"你好", "这是问题"} {
		if err := r.AppendChat(ctx, ChatMessage{ChatID: "g", ProjectID: "p", Role: "user", Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	msgs, err := r.RecentChat(ctx, "g", 10)
	if err != nil || len(msgs) != 2 || msgs[0].Body != "你好" {
		t.Fatalf("chat: %+v err=%v", msgs, err)
	}
}
