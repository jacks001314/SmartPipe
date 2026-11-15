package github

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// 上传接口和普通 REST 接口不在同一个主机路径下，参数与二进制请求体都要对。
func TestUploadUserAttachmentPostsToUserAssets(t *testing.T) {
	var (
		gotPath        string
		gotQuery       url.Values
		gotAuth        string
		gotContentType string
		gotBody        string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/repos/o/r" {
			_, _ = w.Write([]byte(`{"id":4242,"full_name":"o/r","private":true}`))
			return
		}
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"url":"https://github.com/user-attachments/assets/abc"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "ghp_user")
	gotURL, err := c.UploadUserAttachment(context.Background(), "o/r", "shot.png", "image/png", []byte("png-bytes"))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if gotURL != "https://github.com/user-attachments/assets/abc" {
		t.Fatalf("url = %q", gotURL)
	}
	if gotPath != "/user-attachments/assets" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotQuery.Get("name") != "shot.png" || gotQuery.Get("content_type") != "image/png" || gotQuery.Get("repository_id") != "4242" {
		t.Fatalf("query = %v", gotQuery)
	}
	if gotAuth != "Bearer ghp_user" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if gotContentType != "application/octet-stream" {
		t.Fatalf("content-type = %q", gotContentType)
	}
	if gotBody != "png-bytes" {
		t.Fatalf("body = %q", gotBody)
	}
}

func TestUploadUserAttachmentSurfacesGitHubError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/repos/o/r" {
			_, _ = w.Write([]byte(`{"id":4242,"full_name":"o/r"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "ghs_installation")
	_, err := c.UploadUserAttachment(context.Background(), "o/r", "shot.png", "image/png", []byte("png-bytes"))
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v", err)
	}
}
