package releasev2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestS3StorePutAndStatStream(t *testing.T) {
	var gotPath, gotSHA, gotAuth, gotMeta string
	var gotBody []byte
	var gotLength int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			gotPath = r.URL.Path
			gotSHA = r.Header.Get("x-amz-content-sha256")
			gotMeta = r.Header.Get("x-amz-meta-sha256")
			gotAuth = r.Header.Get("Authorization")
			gotLength = r.ContentLength
			gotBody, _ = io.ReadAll(r.Body)
			w.Header().Set("x-amz-meta-sha256", gotMeta)
			w.WriteHeader(http.StatusOK)
		case http.MethodHead:
			w.Header().Set("x-amz-meta-sha256", gotMeta)
			w.Header().Set("Content-Length", "5")
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	store, err := NewS3Store(server.URL, "us-east-1", "releases", "AKIAEXAMPLE", "secret", "", "smartpipe")
	if err != nil {
		t.Fatal(err)
	}
	payload := "artifact"
	obj, err := store.Put(context.Background(), "releases/run_1/app.exe", strings.NewReader(payload), int64(len(payload)), "application/octet-stream")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(payload))
	if obj.SHA256 != "sha256:"+hex.EncodeToString(sum[:]) || obj.Size != int64(len(payload)) {
		t.Fatalf("stored = %+v", obj)
	}
	if gotPath != "/releases/smartpipe/releases/run_1/app.exe" {
		t.Fatalf("path = %s", gotPath)
	}
	if gotSHA != hex.EncodeToString(sum[:]) || gotMeta != obj.SHA256 {
		t.Fatalf("hashes = %q %q", gotSHA, gotMeta)
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/") || !strings.Contains(gotAuth, "SignedHeaders=") {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotLength != int64(len(payload)) || string(gotBody) != payload {
		t.Fatalf("body = %q len=%d", gotBody, gotLength)
	}

	stat, err := store.Stat(context.Background(), "releases/run_1/app.exe")
	if err != nil {
		t.Fatal(err)
	}
	if stat.SHA256 != obj.SHA256 {
		t.Fatalf("stat = %+v", stat)
	}
}

func TestS3StorePresign(t *testing.T) {
	store, err := NewS3Store("https://minio.example.com", "cn-east-1", "releases", "AKIAEXAMPLE", "secret", "token", "smartpipe")
	if err != nil {
		t.Fatal(err)
	}
	url, headers, err := store.PresignPut(context.Background(), "releases/run_1/app.exe", 10, "application/octet-stream", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"X-Amz-Algorithm=AWS4-HMAC-SHA256", "X-Amz-Credential=AKIAEXAMPLE%2F", "X-Amz-Expires=3600",
		"X-Amz-SignedHeaders=host", "X-Amz-Signature=", "X-Amz-Security-Token=token",
		"/releases/smartpipe/releases/run_1/app.exe",
	} {
		if !strings.Contains(url, want) {
			t.Fatalf("presigned url missing %q: %s", want, url)
		}
	}
	if headers == nil {
		t.Fatal("presign headers must not be nil")
	}
	if _, err := store.PresignGet(context.Background(), "releases/run_1/app.exe", time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestS3StoreListAndDelete(t *testing.T) {
	var listQuery, deletePath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			listQuery = r.URL.RawQuery
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult>
  <IsTruncated>false</IsTruncated>
  <Contents>
    <Key>smartpipe/releases/rrun_x/a.zip</Key>
    <Size>5</Size>
    <LastModified>2026-11-12T10:00:00.000Z</LastModified>
  </Contents>
</ListBucketResult>`))
		case http.MethodDelete:
			deletePath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	store, err := NewS3Store(server.URL, "us-east-1", "releases", "AKIAEXAMPLE", "secret", "", "smartpipe")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	items, err := store.List(ctx, "releases/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listQuery, "list-type=2") || !strings.Contains(listQuery, "prefix=smartpipe%2Freleases%2F") {
		t.Fatalf("list query = %s", listQuery)
	}
	if len(items) != 1 || items[0].Key != "releases/rrun_x/a.zip" || items[0].Size != 5 || items[0].ModTime.IsZero() {
		t.Fatalf("items = %+v", items)
	}
	if err := store.Delete(ctx, "releases/rrun_x/a.zip"); err != nil {
		t.Fatal(err)
	}
	if deletePath != "/releases/smartpipe/releases/rrun_x/a.zip" {
		t.Fatalf("delete path = %s", deletePath)
	}
}