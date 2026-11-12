package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testBundle(version, position string) string {
	return fmt.Sprintf(`{"schemaVersion":1,"productVersion":"%s","buildVersion":"%s","channel":"test","components":{"product":{"version":"%s","slots":[{"slot":"app","os":"linux","arch":"amd64","name":"app-%s.tar.gz","size":10,"sha256":"sha256:%s"}]}}}`,
		version, version, version, position, hex.EncodeToString(sha256.New().Sum(nil))[:0]+"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
}

func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func post(t *testing.T, url string, body any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func stageImport(t *testing.T, base, version, bundle, key string) string {
	t.Helper()
	code, out := post(t, base+"/v1/admin/update-imports", map[string]any{
		"product": "fixture", "channel": "test", "productVersion": version, "buildVersion": version,
		"bundleSha256": digestOf(bundle), "bundle": json.RawMessage(bundle), "idempotencyKey": key,
	})
	if code != 200 {
		t.Fatalf("stage %s = %d %v", version, code, out)
	}
	id, _ := out["id"].(string)
	if id == "" || out["state"] != "staged" {
		t.Fatalf("stage response = %v", out)
	}
	if slots, _ := out["slots"].([]any); len(slots) != 1 {
		t.Fatalf("slots = %v", out["slots"])
	}
	return id
}

func latestBOM(t *testing.T, base string) string {
	t.Helper()
	resp, err := http.Get(base + "/v1/distributions/latest?channel=test")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 200 {
		return ""
	}
	s, _ := out["bomSha256"].(string)
	return s
}

func TestStubStagePromoteWithCAS(t *testing.T) {
	s := newStub("", "")
	server := httptest.NewServer(s.handler())
	defer server.Close()

	bundleA := testBundle("0.4.0", "a")
	idA := stageImport(t, server.URL, "0.4.0", bundleA, "stage-a")

	code, _ := post(t, server.URL+"/v1/admin/update-imports", map[string]any{
		"product": "fixture", "channel": "test", "productVersion": "0.4.0",
		"bundleSha256": digestOf(bundleA), "bundle": json.RawMessage(bundleA), "idempotencyKey": "stage-a",
	})
	if code != 200 {
		t.Fatalf("idempotent stage = %d", code)
	}

	// First release: no current BOM, CAS expectation must be empty.
	code, out := post(t, server.URL+"/v1/admin/update-imports/"+idA+"/promote", map[string]any{
		"expectedBomSha256": "sha256:deadbeef", "idempotencyKey": "p-bad",
	})
	if code != http.StatusConflict {
		t.Fatalf("stale expectation promote = %d %v", code, out)
	}
	code, out = post(t, server.URL+"/v1/admin/update-imports/"+idA+"/promote", map[string]any{"idempotencyKey": "p-a"})
	if code != 200 || out["state"] != "promoted" {
		t.Fatalf("first promote = %d %v", code, out)
	}
	if got := latestBOM(t, server.URL); got != digestOf(bundleA) {
		t.Fatalf("latest after A = %q", got)
	}

	bundleB := testBundle("0.4.1", "b")
	idB := stageImport(t, server.URL, "0.4.1", bundleB, "stage-b")
	code, out = post(t, server.URL+"/v1/admin/update-imports/"+idB+"/promote", map[string]any{
		"expectedBomSha256": digestOf(bundleA), "idempotencyKey": "p-b",
	})
	if code != 200 || out["state"] != "promoted" {
		t.Fatalf("promote B = %d %v", code, out)
	}
	if got := latestBOM(t, server.URL); got != digestOf(bundleB) {
		t.Fatalf("latest after B = %q", got)
	}

	bundleC := testBundle("0.4.2", "c")
	idC := stageImport(t, server.URL, "0.4.2", bundleC, "stage-c")
	code, _ = post(t, server.URL+"/v1/admin/update-imports/"+idC+"/promote", map[string]any{
		"expectedBomSha256": digestOf(bundleA), "idempotencyKey": "p-c-stale",
	})
	if code != http.StatusConflict {
		t.Fatalf("stale CAS promote C = %d", code)
	}
}

func TestStubRejectsDigestMismatch(t *testing.T) {
	s := newStub("", "")
	server := httptest.NewServer(s.handler())
	defer server.Close()
	code, out := post(t, server.URL+"/v1/admin/update-imports", map[string]any{
		"product": "fixture", "channel": "test", "productVersion": "0.4.1",
		"bundleSha256": "sha256:0000", "bundle": json.RawMessage(testBundle("0.4.1", "x")), "idempotencyKey": "stage-bad",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("mismatch stage = %d %v", code, out)
	}
}
