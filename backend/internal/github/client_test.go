package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCreateIssueSendsAuthAndLabels(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		buf := new(strings.Builder)
		_, _ = io.Copy(buf, r.Body)
		gotBody = buf.String()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":42,"html_url":"https://github.com/o/r/issues/42","title":"bug"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "tok-123")
	issue, err := c.CreateIssue(context.Background(), "o/r", "崩溃：启动即退", "正文", []string{"source:bluex", "smartpipe"})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if issue.Number != 42 || !strings.Contains(issue.HTMLURL, "/issues/42") {
		t.Fatalf("unexpected issue: %+v", issue)
	}
	if gotPath != "/repos/o/r/issues" {
		t.Fatalf("path = %s", gotPath)
	}
	if gotAuth != "Bearer tok-123" {
		t.Fatalf("auth = %s", gotAuth)
	}
	if !strings.Contains(gotBody, "source:bluex") || !strings.Contains(gotBody, "崩溃") {
		t.Fatalf("body missing fields: %s", gotBody)
	}
}

func TestCommentAndLabelsPaths(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	c := New(srv.URL, "t")
	if err := c.Comment(context.Background(), "o/r", 7, "hi"); err != nil {
		t.Fatalf("comment: %v", err)
	}
	if err := c.AddLabels(context.Background(), "o/r", 7, []string{"x"}); err != nil {
		t.Fatalf("labels: %v", err)
	}
	want := []string{"/repos/o/r/issues/7/comments", "/repos/o/r/issues/7/labels"}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths = %v", paths)
		}
	}
}

func TestSplitRepo(t *testing.T) {
	cases := map[string][2]string{
		"o/r":                        {"o", "r"},
		"https://github.com/o/r":     {"o", "r"},
		"https://github.com/o/r.git": {"o", "r"},
		"git@github.com:o/r.git":     {"o", "r.git"},
	}
	for in, want := range cases {
		o, n, err := SplitRepo(in)
		if err != nil {
			t.Fatalf("SplitRepo(%q): %v", in, err)
		}
		if o != want[0] || strings.TrimSuffix(n, ".git") != strings.TrimSuffix(want[1], ".git") {
			t.Fatalf("SplitRepo(%q) = %q/%q", in, o, n)
		}
	}
	if _, _, err := SplitRepo("bad"); err == nil {
		t.Fatal("expected error for invalid repo")
	}
}

func TestAPIErrorOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Validation Failed"}`))
	}))
	defer srv.Close()
	_, err := New(srv.URL, "t").CreateIssue(context.Background(), "o/r", "x", "y", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnprocessableEntity {
		t.Fatalf("expected APIError 422, got %v", err)
	}
}

func TestSignAppJWTShape(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	token, err := SignAppJWT("12345", string(pemBytes), time.Unix(1700000000, 0))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if parts := strings.Split(token, "."); len(parts) != 3 {
		t.Fatalf("jwt parts = %d", len(parts))
	}
}
