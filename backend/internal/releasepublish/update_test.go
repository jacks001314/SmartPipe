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

func TestUpdateClientStagesAndPublishes(t *testing.T) {
	var (
		gotAuth, gotFilename, gotSHA, gotBody string
		gotComponent, gotPublish              string
		gotDistribution                       map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/components":
			writeTestJSON(w, map[string]any{"name": "product", "kind": "product", "versioning": "product"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/admin/releases":
			writeTestJSON(w, map[string]any{"releases": []map[string]any{
				{"id": "release-1", "component": "product", "version": "0.5.16", "channel": "beta"},
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/releases":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotComponent = body["component"].(string)
			writeTestJSON(w, map[string]any{"id": "release-2", "component": body["component"], "version": body["version"], "channel": body["channel"]})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v1/admin/releases/release-2/artifacts/"):
			gotAuth = r.Header.Get("Authorization")
			gotFilename = r.Header.Get("X-Filename")
			gotSHA = r.Header.Get("X-Sha256")
			raw, _ := io.ReadAll(r.Body)
			gotBody = string(raw)
			if r.URL.Path != "/v1/admin/releases/release-2/artifacts/desktop/windows/amd64" {
				t.Errorf("upload path = %s", r.URL.Path)
			}
			writeTestJSON(w, map[string]any{"id": "artifact-1", "releaseId": "release-2", "slot": "desktop", "os": "windows", "arch": "amd64", "filename": gotFilename, "size": len(raw), "sha256": gotSHA})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/releases/release-2/publish":
			gotPublish = "yes"
			writeTestJSON(w, map[string]string{"releaseId": "release-2", "status": "published"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/distributions":
			_ = json.NewDecoder(r.Body).Decode(&gotDistribution)
			writeTestJSON(w, map[string]any{"channel": "beta", "productVersion": "0.5.16", "components": []map[string]any{
				{"name": "product", "version": "0.5.16", "releaseId": "release-2"},
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/distributions/latest":
			payload := map[string]any{
				"channel": "beta", "productVersion": "0.5.16", "publishedAt": "2026-11-12T00:00:00Z",
				"components": []map[string]any{{"name": "product", "version": "0.5.16"}},
			}
			raw, _ := json.Marshal(payload)
			writeTestJSON(w, map[string]any{"payload": json.RawMessage(raw), "signature": "sig"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	client := &UpdateClient{BaseURL: server.URL, Creds: testCreds()}

	if _, err := client.EnsureComponent(ctx, "update-stable", EnsureComponentRequest{Name: "product", Kind: "product", Versioning: "product"}); err != nil {
		t.Fatal(err)
	}
	if release, err := client.FindRelease(ctx, "update-stable", "product", "beta", "0.5.16"); err != nil || release.ID != "release-1" {
		t.Fatalf("find release = %+v, %v", release, err)
	}
	if _, err := client.FindRelease(ctx, "update-stable", "product", "beta", "9.9.9"); !errors.Is(err, ErrNotFoundRemote) {
		t.Fatalf("missing release err = %v", err)
	}
	created, err := client.CreateRelease(ctx, "update-stable", CreateReleaseRequest{Component: "product", Version: "0.5.17", Channel: "beta"})
	if err != nil || created.ID != "release-2" || gotComponent != "product" {
		t.Fatalf("create release = %+v, %v", created, err)
	}
	if _, err := client.UploadArtifact(ctx, "update-stable", "release-2", "desktop", "windows", "amd64", "app-setup.exe",
		"sha256:abc123", int64(len("payload")), strings.NewReader("payload")); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer s3cret" || gotFilename != "app-setup.exe" || gotSHA != "abc123" || gotBody != "payload" {
		t.Fatalf("upload headers = %q %q %q body=%q", gotAuth, gotFilename, gotSHA, gotBody)
	}
	if err := client.PublishRelease(ctx, "update-stable", "release-2"); err != nil || gotPublish != "yes" {
		t.Fatalf("publish release err = %v", err)
	}
	if _, err := client.PublishDistribution(ctx, "update-stable", PublishDistributionRequest{
		Channel: "beta", ProductVersion: "0.5.16",
		Components: []DistributionComponent{{Name: "product", Version: "0.5.16"}},
	}); err != nil {
		t.Fatal(err)
	}
	if gotDistribution["productVersion"] != "0.5.16" {
		t.Fatalf("distribution body = %+v", gotDistribution)
	}
	pointer, err := client.Latest(ctx, "", "beta")
	if err != nil {
		t.Fatal(err)
	}
	want := DistributionDigest("beta", "0.5.16", []DistributionComponent{{Name: "product", Version: "0.5.16"}})
	if pointer.ProductVersion != "0.5.16" || pointer.BOMSHA256 != want {
		t.Fatalf("pointer = %+v want digest %s", pointer, want)
	}
}

func TestUpdateClientRejectsMissingCredential(t *testing.T) {
	client := &UpdateClient{BaseURL: "https://example.invalid", Creds: testCreds()}
	_, err := client.EnsureComponent(context.Background(), "update-missing", EnsureComponentRequest{Name: "product"})
	if !errors.Is(err, ErrCredentialMissing) {
		t.Fatalf("err = %v", err)
	}
}

func TestDistributionDigestIgnoresOrderAndExpiry(t *testing.T) {
	left := DistributionDigest("stable", "1.2.3", []DistributionComponent{{Name: "skills", Version: "1.0.0"}, {Name: "product", Version: "1.2.3"}})
	right := DistributionDigest("stable", "1.2.3", []DistributionComponent{{Name: "product", Version: "1.2.3"}, {Name: "skills", Version: "1.0.0"}})
	if left != right {
		t.Fatalf("digest depends on component order: %s vs %s", left, right)
	}
	if left == DistributionDigest("beta", "1.2.3", []DistributionComponent{{Name: "product", Version: "1.2.3"}}) {
		t.Fatal("digest must include channel and component set")
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

func writeTestJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
