package releasesvc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"smartpipe/backend/internal/releasev2"
)

func TestPutTargetManifestAssemblesRunManifest(t *testing.T) {
	objects, err := releasev2.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run := &releasev2.Run{ID: "run_1", Status: releasev2.RunBuilding, Version: "1.0.0", ResolvedCommit: strings.Repeat("c", 40)}
	fake := &fakeControlStore{
		run: run,
		target: &releasev2.Target{
			RunID: "run_1", TargetID: "mac-arm64", OS: "darwin", Arch: "arm64", Status: releasev2.TargetBuilding,
		},
	}
	handler := &HTTPHandler{Store: fake, Objects: objects, Token: "svc-token"}
	server := httptest.NewServer(handler.Handler())
	defer server.Close()

	manifest := releasev2.ArtifactManifest{
		Schema: releasev2.ArtifactManifestSchema, RunID: "run_1", TargetID: "mac-arm64",
		OS: "darwin", Arch: "arm64", Version: "1.0.0", Commit: run.ResolvedCommit,
		Artifacts: []releasev2.ManifestArtifact{{
			Role: "installer", Kind: "installer", Path: "dist/app.dmg", Name: "app.dmg",
			Size: 42, SHA256: "sha256:" + strings.Repeat("a", 64), Required: true,
		}},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, server.URL+"/internal/release-executors/mac-01/manifest", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer svc-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if fake.target.ManifestPath != "manifests/run_1/mac-arm64.json" {
		t.Fatalf("target manifest path = %q", fake.target.ManifestPath)
	}
	if run.ManifestStorageKey != "manifests/run_1/run.json" {
		t.Fatalf("run manifest key = %q", run.ManifestStorageKey)
	}
	obj, err := objects.Open(context.Background(), run.ManifestStorageKey)
	if err != nil {
		t.Fatal(err)
	}
	defer obj.Close()
	var merged releasev2.RunArtifactManifest
	if err := json.NewDecoder(obj).Decode(&merged); err != nil {
		t.Fatal(err)
	}
	if len(merged.Targets) != 1 || merged.Targets[0].TargetID != "mac-arm64" {
		t.Fatalf("merged targets = %+v", merged.Targets)
	}
	destinations := merged.Targets[0].Artifacts[0].Destinations
	if strings.Join(destinations, ",") != "github,update:full" {
		t.Fatalf("destinations = %v", destinations)
	}
}
