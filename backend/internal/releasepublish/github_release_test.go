package releasepublish

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 同版本重发要求按 tag 找到已有 Release 并能替换旧附件。
func TestGetReleaseByTagAndDeleteAsset(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/app/releases/tags/v0.5.17":
			writeTestJSON(w, map[string]any{"id": 11, "tag_name": "v0.5.17", "draft": false})
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/acme/app/releases/assets/9":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &GitHubClient{APIBase: server.URL, UploadBase: server.URL, Creds: EnvProvider{Lookup: func(name string) (string, bool) {
		if name == "SMARTPIPE_CRED_GITHUB" {
			return "ghtoken", true
		}
		return "", false
	}}}
	ctx := context.Background()
	release, err := client.GetReleaseByTag(ctx, "github", "acme", "app", "v0.5.17")
	if err != nil {
		t.Fatalf("GetReleaseByTag: %v", err)
	}
	if release.ID != 11 || release.Draft {
		t.Fatalf("release = %+v", release)
	}
	if err := client.DeleteAsset(ctx, "github", "acme", "app", 9); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}
	if _, err := client.GetReleaseByTag(ctx, "github", "acme", "app", "v9.9.9"); !errors.Is(err, ErrNotFoundRemote) {
		t.Fatalf("missing tag error = %v, want ErrNotFoundRemote", err)
	}
	want := []string{
		"GET /repos/acme/app/releases/tags/v0.5.17",
		"DELETE /repos/acme/app/releases/assets/9",
		"GET /repos/acme/app/releases/tags/v9.9.9",
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v", calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls = %v, want %v", calls, want)
		}
	}
}
