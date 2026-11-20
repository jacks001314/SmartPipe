package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCommitsBetweenReadsComparePages(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`{"commits":[{"sha":"c","commit":{"message":"fix: third\n\nbody"}}]}`))
			return
		}
		w.Header().Set("Link", `<http://`+r.Host+r.URL.Path+`?page=2>; rel="next"`)
		_, _ = w.Write([]byte(`{"commits":[{"sha":"a","commit":{"message":"feat: first"}},{"sha":"b","commit":{"message":"fix: second"}}]}`))
	}))
	defer srv.Close()

	client := New(srv.URL, "tok")
	commits, err := client.CommitsBetween(context.Background(), "o/r", "v1", "v2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 3 {
		t.Fatalf("commits = %d, want 3: %+v", len(commits), commits)
	}
	if commits[0].Message != "feat: first" || !strings.HasPrefix(commits[2].Message, "fix: third") {
		t.Fatalf("unexpected commits: %+v", commits)
	}
	if len(paths) != 2 || !strings.HasSuffix(paths[0], "/compare/v1...v2") {
		t.Fatalf("paths = %v", paths)
	}
}
