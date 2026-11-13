package releasesvc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"smartpipe/backend/internal/releasev2"
	"smartpipe/backend/internal/store"
)

type fakeControlStore struct {
	target      *releasev2.Target
	artifacts   []releasev2.Artifact
	statusCalls []string
	events      []releasev2.Event

	requeueOK    bool
	requeueCalls []string
	finishCalls  []string
}

func (f *fakeControlStore) GetExecutor(context.Context, string) (*releasev2.Executor, error) {
	return &releasev2.Executor{ID: "win-01", OS: "windows", Arch: "amd64"}, nil
}
func (f *fakeControlStore) UpsertExecutor(context.Context, releasev2.Executor) error { return nil }
func (f *fakeControlStore) ClaimReleaseV2Target(context.Context, string, string, string, time.Duration) (*releasev2.Target, error) {
	return nil, nil
}
func (f *fakeControlStore) GetRun(context.Context, string) (*releasev2.Run, error) {
	return &releasev2.Run{ID: "run_1", Status: releasev2.RunBuilding}, nil
}

func (f *fakeControlStore) GetProject(context.Context, string) (*store.Project, error) {
	return &store.Project{ID: "prj_1", LocalRepoPath: ""}, nil
}
func (f *fakeControlStore) GetTarget(context.Context, string, string) (*releasev2.Target, error) {
	copyTarget := *f.target
	return &copyTarget, nil
}
func (f *fakeControlStore) HeartbeatReleaseV2Target(context.Context, string, string, string, time.Duration) (bool, error) {
	return true, nil
}
func (f *fakeControlStore) FinishReleaseV2Target(_ context.Context, runID, targetID string, _ string, ok bool, code, message string) (bool, error) {
	f.finishCalls = append(f.finishCalls, runID+"/"+targetID+" ok="+boolString(ok)+" code="+code)
	return true, nil
}

func (f *fakeControlStore) RequeueReleaseV2TargetLease(_ context.Context, runID, targetID, _ string, code, message string, maxAttempt int) (bool, error) {
	f.requeueCalls = append(f.requeueCalls, runID+"/"+targetID+" code="+code)
	return f.requeueOK, nil
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
func (f *fakeControlStore) MarkTargetStatus(_ context.Context, _, _ string, from, to releasev2.TargetStatus) (bool, error) {
	f.statusCalls = append(f.statusCalls, string(from)+"->"+string(to))
	return true, nil
}
func (f *fakeControlStore) AppendEvent(_ context.Context, e releasev2.Event) error {
	f.events = append(f.events, e)
	return nil
}
func (f *fakeControlStore) AppendLogChunk(context.Context, string, string, string, int, string) error {
	return nil
}
func (f *fakeControlStore) AddArtifact(_ context.Context, a releasev2.Artifact) (*releasev2.Artifact, error) {
	a.ID = int64(len(f.artifacts) + 1)
	f.artifacts = append(f.artifacts, a)
	return &a, nil
}

func TestArtifactRelayUploadRegistersDigest(t *testing.T) {
	dir := t.TempDir()
	objects, err := releasev2.NewLocalStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeControlStore{target: &releasev2.Target{
		RunID: "run_1", TargetID: "windows-amd64", Attempt: 1, Status: releasev2.TargetBuilding,
		Artifacts: []releasev2.ArtifactRule{{Role: "installer", Glob: "dist/*.exe", ContentType: "application/x-msdownload", Required: true}},
	}}
	handler := &HTTPHandler{Store: fake, Objects: objects, Token: "svc-token"}
	server := httptest.NewServer(handler.Handler())
	defer server.Close()

	payload := bytes.Repeat([]byte("syntropy-installer\n"), 4096)
	beginBody, _ := json.Marshal(map[string]any{
		"run_id": "run_1", "target_id": "windows-amd64", "attempt": 1, "role": "installer",
		"name": "SyntropySetup.exe", "size": len(payload),
	})
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/internal/release-executors/win-01/artifacts/begin", bytes.NewReader(beginBody))
	req.Header.Set("Authorization", "Bearer svc-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	beginRaw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("begin = %d %s", resp.StatusCode, beginRaw)
	}
	var ticket artifactBeginResponse
	if err := json.Unmarshal(beginRaw, &ticket); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ticket.UploadURL, "/internal/release-executors/win-01/artifacts/put") {
		t.Fatalf("upload url = %s", ticket.UploadURL)
	}

	putReq, _ := http.NewRequest(http.MethodPut, server.URL+ticket.UploadURL, bytes.NewReader(payload))
	putReq.Header.Set("Authorization", "Bearer svc-token")
	putReq.ContentLength = int64(len(payload))
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatal(err)
	}
	putRaw, _ := io.ReadAll(putResp.Body)
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusCreated {
		t.Fatalf("put = %d %s", putResp.StatusCode, putRaw)
	}
	var artifact releasev2.Artifact
	if err := json.Unmarshal(putRaw, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.SHA256 != releasev2.HashText(string(payload)) || artifact.Size != int64(len(payload)) {
		t.Fatalf("artifact = %+v", artifact)
	}
	if artifact.Role != "installer" || artifact.StorageKey == "" || artifact.ContentType != "application/x-msdownload" {
		t.Fatalf("artifact metadata = %+v", artifact)
	}
	if len(fake.artifacts) != 1 || len(fake.events) != 1 || fake.events[0].Kind != "artifact.uploaded" {
		t.Fatalf("store artifacts/events = %d %+v", len(fake.artifacts), fake.events)
	}
	if len(fake.statusCalls) == 0 || fake.statusCalls[0] != "building->uploaded" {
		t.Fatalf("status calls = %v", fake.statusCalls)
	}
	stat, err := objects.Stat(context.Background(), artifact.StorageKey)
	if err != nil || stat.SHA256 != artifact.SHA256 {
		t.Fatalf("stored object = %+v err=%v", stat, err)
	}
}

func TestArtifactBeginRejectsUndeclaredRoleAndSize(t *testing.T) {
	objects, _ := releasev2.NewLocalStore(t.TempDir())
	fake := &fakeControlStore{target: &releasev2.Target{
		RunID: "run_1", TargetID: "windows-amd64", Status: releasev2.TargetBuilding,
		Artifacts: []releasev2.ArtifactRule{{Role: "installer", Glob: "dist/*.exe"}},
	}}
	handler := &HTTPHandler{Store: fake, Objects: objects}
	server := httptest.NewServer(handler.Handler())
	defer server.Close()

	for _, body := range []string{
		fmt.Sprintf(`{"run_id":"run_1","target_id":"windows-amd64","role":"blockmap","name":"a.exe","size":10}`),
		fmt.Sprintf(`{"run_id":"run_1","target_id":"windows-amd64","role":"installer","name":"a.exe","size":0}`),
	} {
		resp, err := http.Post(server.URL+"/internal/release-executors/win-01/artifacts/begin", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("begin %s = %d", body, resp.StatusCode)
		}
	}
}

func TestArtifactConfirmRegistersDirectUpload(t *testing.T) {
	objects, _ := releasev2.NewLocalStore(t.TempDir())
	fake := &fakeControlStore{target: &releasev2.Target{
		RunID: "run_1", TargetID: "linux-amd64", Attempt: 1, Status: releasev2.TargetBuilding,
		Artifacts: []releasev2.ArtifactRule{{Role: "app-bundle", Glob: "dist/*.tar.gz"}},
	}}
	handler := &HTTPHandler{Store: fake, Objects: objects}
	server := httptest.NewServer(handler.Handler())
	defer server.Close()

	payload := []byte("direct-upload-payload")
	key := "releases/run_1/linux-amd64/1/app-bundle/app.tar.gz"
	stored, err := objects.Put(context.Background(), key, bytes.NewReader(payload), int64(len(payload)), "application/gzip")
	if err != nil {
		t.Fatal(err)
	}
	confirmBody, _ := json.Marshal(map[string]any{
		"run_id": "run_1", "target_id": "linux-amd64", "attempt": 1, "role": "app-bundle",
		"name": "app.tar.gz", "size": len(payload), "content_type": "application/gzip", "sha256": stored.SHA256,
	})
	resp, err := http.Post(server.URL+"/internal/release-executors/linux-01/artifacts/confirm", "application/json", bytes.NewReader(confirmBody))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("confirm = %d %s", resp.StatusCode, raw)
	}
	var artifact releasev2.Artifact
	if err := json.Unmarshal(raw, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.SHA256 != stored.SHA256 || artifact.Size != int64(len(payload)) || artifact.StorageKey != key {
		t.Fatalf("artifact = %+v", artifact)
	}

	badBody, _ := json.Marshal(map[string]any{
		"run_id": "run_1", "target_id": "linux-amd64", "attempt": 1, "role": "app-bundle",
		"name": "app.tar.gz", "size": len(payload), "sha256": "sha256:" + strings.Repeat("f", 64),
	})
	resp2, _ := http.Post(server.URL+"/internal/release-executors/linux-01/artifacts/confirm", "application/json", bytes.NewReader(badBody))
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("digest mismatch confirm = %d", resp2.StatusCode)
	}
}

func TestArtifactSignedPerRole(t *testing.T) {
	yes, no := true, false
	target := &releasev2.Target{
		SignCommand: "signtool sign ...",
		Artifacts: []releasev2.ArtifactRule{
			{Role: "app-bundle"},
			{Role: "debug-symbol", Signed: &no},
			{Role: "manifest", Signed: &yes},
		},
	}
	cases := map[string]bool{"app-bundle": true, "debug-symbol": false, "manifest": true, "unknown": true}
	for role, want := range cases {
		if got := artifactSigned(target, role); got != want {
			t.Fatalf("artifactSigned(%s) = %v, want %v", role, got, want)
		}
	}
	unsignedTarget := &releasev2.Target{Artifacts: target.Artifacts}
	if artifactSigned(unsignedTarget, "app-bundle") {
		t.Fatal("target without sign command must never report signed artifacts")
	}
	if artifactSigned(nil, "app-bundle") {
		t.Fatal("nil target must not report signed artifacts")
	}
}
