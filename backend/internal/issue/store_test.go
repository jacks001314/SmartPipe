package issue

import (
	"context"
	"errors"
	"testing"
)

func TestMemoryStoreLifecycle(t *testing.T) {
	st := NewMemoryStore()
	ctx := context.Background()

	iss, err := st.Create(ctx, CreateInput{Title: "登录失败", Description: "点击登录无响应", Reporter: Reporter{UserID: "u1", UserName: "张三"}})
	if err != nil || iss.ID == "" || iss.Status != StatusNew {
		t.Fatalf("create: %v %+v", err, iss)
	}

	got, err := st.Get(ctx, iss.ID)
	if err != nil || got.Title != "登录失败" {
		t.Fatalf("get: %v %+v", err, got)
	}

	if _, err := st.AddComment(ctx, iss.ID, "orchestrator", "归并测试"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateStatus(ctx, iss.ID, StatusTriaged); err != nil {
		t.Fatal(err)
	}
	got, _ = st.Get(ctx, iss.ID)
	if got.Status != StatusTriaged || len(got.Comments) != 1 {
		t.Fatalf("update: %+v", got)
	}

	// 检索命中
	hits, err := st.Search(ctx, "登录失败 无响应", 5)
	if err != nil || len(hits) == 0 || hits[0].ID != iss.ID {
		t.Fatalf("search: %v %+v", err, hits)
	}

	if _, err := st.Get(ctx, "ISS-X-9999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
