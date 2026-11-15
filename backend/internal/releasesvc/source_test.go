package releasesvc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"smartpipe/backend/internal/releasev2"
)

func TestFreezeSourceFromUploadAndServeFrozen(t *testing.T) {
	objects, err := releasev2.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("local store: %v", err)
	}
	fake := &fakeControlStore{run: &releasev2.Run{
		ID: "run_1", ProjectID: "prj_1", Status: releasev2.RunBuilding, ResolvedCommit: "abc123",
	}}
	handler := &HTTPHandler{Store: fake, Objects: objects}
	srv := httptest.NewServer(handler.Handler())
	defer srv.Close()

	body := []byte("fake source archive")
	resp, err := http.Post(srv.URL+"/internal/release-runs/run_1/source", "application/gzip", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("freeze status=%d body=%s", resp.StatusCode, payload)
	}
	var frozen struct {
		StorageKey string `json:"storage_key"`
		SHA256     string `json:"sha256"`
		Size       int64  `json:"size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&frozen); err != nil {
		t.Fatalf("decode freeze response: %v", err)
	}
	sum := sha256.Sum256(body)
	wantSHA := "sha256:" + hex.EncodeToString(sum[:])
	if frozen.SHA256 != wantSHA {
		t.Fatalf("sha256=%q want %q", frozen.SHA256, wantSHA)
	}
	if frozen.StorageKey != "sources/run_1.tar.gz" {
		t.Fatalf("storage key=%q", frozen.StorageKey)
	}
	if fake.run.SourceStorageKey != frozen.StorageKey || fake.run.SourceSHA256 != wantSHA {
		t.Fatalf("run not updated: %+v", fake.run)
	}

	served, err := http.Get(srv.URL + "/internal/release-executors/win-01/source?run_id=run_1&commit=abc123")
	if err != nil {
		t.Fatalf("serve frozen: %v", err)
	}
	defer served.Body.Close()
	if served.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(served.Body)
		t.Fatalf("serve status=%d body=%s", served.StatusCode, payload)
	}
	if got := served.Header.Get("X-Source-Frozen"); got != "1" {
		t.Fatalf("X-Source-Frozen=%q", got)
	}
	got, _ := io.ReadAll(served.Body)
	if !bytes.Equal(got, body) {
		t.Fatalf("served body=%q want %q", got, body)
	}
}

func TestFreezeSourceIsIdempotent(t *testing.T) {
	objects, err := releasev2.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("local store: %v", err)
	}
	fake := &fakeControlStore{run: &releasev2.Run{ID: "run_2", Status: releasev2.RunBuilding, ResolvedCommit: "def"}}
	handler := &HTTPHandler{Store: fake, Objects: objects}
	srv := httptest.NewServer(handler.Handler())
	defer srv.Close()

	if _, err := http.Post(srv.URL+"/internal/release-runs/run_2/source", "application/gzip", bytes.NewReader([]byte("first"))); err != nil {
		t.Fatalf("first freeze: %v", err)
	}
	resp, err := http.Post(srv.URL+"/internal/release-runs/run_2/source", "application/gzip", bytes.NewReader([]byte("second")))
	if err != nil {
		t.Fatalf("second freeze: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		AlreadyFrozen bool `json:"already_frozen"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.AlreadyFrozen {
		t.Fatalf("second freeze should be a no-op")
	}
}
