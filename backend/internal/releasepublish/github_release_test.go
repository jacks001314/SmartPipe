package releasepublish

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 稳定版发布必须显式声明 make_latest，否则同一个 tag 先由 beta 建过
// prerelease 后，GitHub 的 Latest 会一直停在上一版稳定版。
func TestUpdateReleaseClaimsLatestForStable(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/repos/acme/app/releases/11" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode patch body: %v", err)
		}
		writeTestJSON(w, map[string]any{"id": 11, "tag_name": "v0.5.18", "draft": false, "prerelease": false})
	}))
	defer server.Close()

	client := &GitHubClient{APIBase: server.URL, UploadBase: server.URL, Creds: EnvProvider{Lookup: func(name string) (string, bool) {
		if name == "SMARTPIPE_CRED_GITHUB" {
			return "ghtoken", true
		}
		return "", false
	}}}
	prerelease := false
	release, err := client.UpdateRelease(context.Background(), "github", "acme", "app", 11,
		PublishOptions{Prerelease: &prerelease, MakeLatest: "true"})
	if err != nil {
		t.Fatalf("UpdateRelease: %v", err)
	}
	if release.Prerelease {
		t.Fatalf("release = %+v, want non-prerelease", release)
	}
	if body["draft"] != false || body["prerelease"] != false || body["make_latest"] != "true" {
		t.Fatalf("patch body = %#v", body)
	}
}

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
