package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func writeTarGz(t *testing.T, files map[string]string) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return bytes.NewReader(buf.Bytes())
}

func TestExtractTarGzStripsGitHubRootDirectory(t *testing.T) {
	dir := t.TempDir()
	archive := writeTarGz(t, map[string]string{
		"Ch3nYe-Syntropy-abc123/package.json":       `{"version":"0.5.17"}`,
		"Ch3nYe-Syntropy-abc123/desktop_app/app.js": "console.log(1)",
	})
	if err := extractTarGz(archive, dir); err != nil {
		t.Fatalf("extract: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		t.Fatalf("package.json: %v", err)
	}
	if string(raw) != `{"version":"0.5.17"}` {
		t.Fatalf("package.json=%q", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, "desktop_app", "app.js")); err != nil {
		t.Fatalf("desktop_app/app.js: %v", err)
	}
}

func TestExtractTarGzKeepsRepoRootLayout(t *testing.T) {
	dir := t.TempDir()
	archive := writeTarGz(t, map[string]string{
		"package.json":       `{"version":"0.5.17"}`,
		"desktop_app/app.js": "console.log(1)",
	})
	if err := extractTarGz(archive, dir); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "desktop_app", "app.js")); err != nil {
		t.Fatalf("desktop_app/app.js: %v", err)
	}
}

// GitHub tarball 会先写一条 pax_global_header（type 'g'）。它没有目录层级，
// 不能被当成“仓库根有文件”的证据，否则唯一顶层目录不会被剥离。
func TestExtractTarGzStripsGithubRootWithPaxHeader(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeXGlobalHeader,
		PAXRecords: map[string]string{
			"comment": "f5d4af36b117a043f2314c4effb3f52669c4d22d",
		},
	}); err != nil {
		t.Fatalf("write pax header: %v", err)
	}
	for name, content := range map[string]string{
		"Ch3nYe-Syntropy-abc123/package.json":       `{"version":"0.5.17"}`,
		"Ch3nYe-Syntropy-abc123/desktop_app/app.js": "console.log(1)",
	} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := extractTarGz(bytes.NewReader(buf.Bytes()), dir); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
		t.Fatalf("package.json should be at repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "desktop_app", "app.js")); err != nil {
		t.Fatalf("desktop_app/app.js: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Ch3nYe-Syntropy-abc123")); !os.IsNotExist(err) {
		t.Fatalf("github root directory should be stripped, stat err=%v", err)
	}
}
