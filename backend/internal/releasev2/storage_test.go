package releasev2

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalStoreStreamsAndHashes(t *testing.T) {
	store, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	payload := strings.Repeat("artifact-bytes\n", 1000)
	obj, err := store.Put(ctx, "releases/run_1/app/installer.exe", strings.NewReader(payload), int64(len(payload)), "application/octet-stream")
	if err != nil {
		t.Fatal(err)
	}
	if obj.SHA256 != HashText(payload) || obj.Size != int64(len(payload)) {
		t.Fatalf("stored = %+v", obj)
	}
	stat, err := store.Stat(ctx, obj.Key)
	if err != nil {
		t.Fatal(err)
	}
	if stat.SHA256 != obj.SHA256 || stat.Size != obj.Size {
		t.Fatalf("stat = %+v", stat)
	}
	rc, err := store.Open(ctx, obj.Key)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != payload {
		t.Fatal("roundtrip payload differs")
	}
	if _, err := os.Stat(filepath.Join(store.Root, "releases", "run_1", "app", "installer.exe")); err != nil {
		t.Fatalf("object file missing: %v", err)
	}
}

func TestLocalStoreRejectsSizeMismatchAndUnsafeKeys(t *testing.T) {
	store, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := store.Put(ctx, "a/b.bin", strings.NewReader("short"), 10, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("size mismatch err = %v", err)
	}
	for _, key := range []string{"../escape", "/abs/path", "a/../../b"} {
		if _, err := store.Put(ctx, key, strings.NewReader("x"), 1, ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("key %q err = %v", key, err)
		}
	}
}

func TestLocalStorePresignRequiresPublicBase(t *testing.T) {
	store, _ := NewLocalStore(t.TempDir())
	ctx := context.Background()
	if _, err := store.PresignGet(ctx, "a/b.bin", 0); err == nil {
		t.Fatal("expected presign error without public base")
	}
	store.PublicURL = "https://objects.example.com/"
	url, err := store.PresignGet(ctx, "releases/a.bin", 0)
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://objects.example.com/releases/a.bin" {
		t.Fatalf("url = %s", url)
	}
}

func TestLocalStoreListAndDelete(t *testing.T) {
	store, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, key := range []string{"releases/rrun_a/t/app.zip", "releases/rrun_a/t/app.sig", "releases/rrun_b/t/app.zip"} {
		if _, err := store.Put(ctx, key, strings.NewReader("x"), 1, ""); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.List(ctx, "releases/rrun_a/")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("listed %d objects, want 2: %+v", len(items), items)
	}
	for _, item := range items {
		if !strings.HasPrefix(item.Key, "releases/rrun_a/") || item.ModTime.IsZero() {
			t.Fatalf("unexpected item %+v", item)
		}
	}
	if err := store.Delete(ctx, "releases/rrun_a/t/app.zip"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, "releases/rrun_a/t/app.zip"); err != nil {
		t.Fatalf("delete twice should be idempotent: %v", err)
	}
	items, _ = store.List(ctx, "releases/rrun_a/")
	if len(items) != 1 {
		t.Fatalf("after delete listed %d, want 1", len(items))
	}
	if items, err := store.List(ctx, "releases/none/"); err != nil || len(items) != 0 {
		t.Fatalf("missing prefix should be empty: %v %v", items, err)
	}
}