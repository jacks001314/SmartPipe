package releasepublish

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testCreds() Provider {
	return EnvProvider{Lookup: func(name string) (string, bool) {
		if name == "SMARTPIPE_CRED_UPDATE_STABLE" {
			return "s3cret", true
		}
		return "", false
	}}
}

func TestUpdateClientStageAndPromote(t *testing.T) {
	var stageAuth, stageKey string
	var stageBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/update-imports":
			stageAuth = r.Header.Get("Authorization")
			stageKey = r.Header.Get("Idempotency-Key")
			_ = json.NewDecoder(r.Body).Decode(&stageBody)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "imp_1", "state": "staged", "channel": "stable", "productVersion": "0.5.16",
				"bomSha256": "sha256:bom1",
				"slots":     []map[string]any{{"slot": "desktop", "os": "windows", "arch": "amd64", "sha256": "sha256:a"}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/update-imports/imp_1/promote":
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "promoted", "channel": "stable", "productVersion": "0.5.16", "bomSha256": "sha256:bom1", "releaseId": "rel_1"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/update-imports/imp_conflict/promote":
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "channel moved by another release"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &UpdateClient{BaseURL: server.URL, Creds: testCreds()}
	imp, err := client.Stage(context.Background(), "update-stable", ImportRequest{
		Product: "product", Channel: "stable", ProductVersion: "0.5.16", BuildVersion: "0.5.16.2026-11-12",
		BundleSHA256: "sha256:bundle", IdempotencyKey: "stage-run_1", BundleURL: "https://objects/x.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if imp.ID != "imp_1" || imp.State != "staged" || len(imp.Slots) != 1 {
		t.Fatalf("import = %+v", imp)
	}
	if stageAuth != "Bearer s3cret" {
		t.Fatalf("authorization = %q", stageAuth)
	}
	if stageKey != "stage-run_1" || stageBody["bundleUrl"] != "https://objects/x.json" {
		t.Fatalf("request headers/body = %q %+v", stageKey, stageBody)
	}

	promoted, err := client.Promote(context.Background(), "update-stable", "imp_1", PromoteRequest{
		ExpectedCurrentProductVersion: "0.5.15", ExpectedBOMSHA256: "sha256:prev", IdempotencyKey: "promote-run_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if promoted.State != "promoted" || promoted.ReleaseID != "rel_1" {
		t.Fatalf("promote = %+v", promoted)
	}

	_, err = client.Promote(context.Background(), "update-stable", "imp_conflict", PromoteRequest{
		ExpectedBOMSHA256: "sha256:x", IdempotencyKey: "promote-2",
	})
	if !errors.Is(err, ErrConflictRemote) {
		t.Fatalf("conflict err = %v", err)
	}
}

func TestUpdateClientRejectsMissingCredential(t *testing.T) {
	client := &UpdateClient{BaseURL: "https://example.invalid", Creds: testCreds()}
	_, err := client.Stage(context.Background(), "update-missing", ImportRequest{
		Product: "product", Channel: "stable", ProductVersion: "1.0.0",
		BundleSHA256: "sha256:x", IdempotencyKey: "k",
	})
	if !errors.Is(err, ErrCredentialMissing) {
		t.Fatalf("err = %v", err)
	}
}

func TestGitHubUploadStreamsWithDigest(t *testing.T) {
	var gotName, gotAuth, gotType string
	var gotLen int64
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			gotName = r.URL.Query().Get("name")
			gotAuth = r.Header.Get("Authorization")
			gotType = r.Header.Get("Content-Type")
			gotLen = r.ContentLength
			raw, _ := io.ReadAll(r.Body)
			gotBody = string(raw)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "name": gotName, "size": len(gotBody), "state": "uploaded", "digest": "sha256:abc"})
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
	asset, err := client.UploadAsset(context.Background(), "github", "acme", "app", 42, "SyntropySetup.exe",
		"application/octet-stream", strings.NewReader("payload"), int64(len("payload")))
	if err != nil {
		t.Fatal(err)
	}
	if asset.ID != 7 || asset.Digest != "sha256:abc" {
		t.Fatalf("asset = %+v", asset)
	}
	if gotName != "SyntropySetup.exe" || gotAuth != "Bearer ghtoken" || gotLen != int64(len("payload")) || gotType != "application/octet-stream" {
		t.Fatalf("upload metadata = %q %q %d %q", gotName, gotAuth, gotLen, gotType)
	}
	if gotBody != "payload" {
		t.Fatalf("body = %q", gotBody)
	}
}
