package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDownloadArchive(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/x-gzip")
		_, _ = w.Write([]byte("archive-bytes"))
	}))
	defer srv.Close()

	archive, err := New(srv.URL, "tok-1").DownloadArchive(context.Background(), "owner/repo", "v1.2.3")
	if err != nil {
		t.Fatalf("DownloadArchive: %v", err)
	}
	defer archive.Body.Close()
	body, _ := io.ReadAll(archive.Body)
	if string(body) != "archive-bytes" {
		t.Fatalf("body=%q", body)
	}
	if gotPath != "/repos/owner/repo/tarball/v1.2.3" {
		t.Fatalf("path=%q", gotPath)
	}
	if gotAuth != "Bearer tok-1" {
		t.Fatalf("auth=%q", gotAuth)
	}
	if archive.ContentType != "application/x-gzip" {
		t.Fatalf("content type=%q", archive.ContentType)
	}
}

func TestDownloadArchiveNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := New(srv.URL, "").DownloadArchive(context.Background(), "owner/repo", "missing")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("err=%v", err)
	}
}
