package runtime

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// 辅助进程：测试二进制在 SMARTPIPE_FAKE_APPSERVER=1 时扮演一个最小 App Server。
func TestMain(m *testing.M) {
	if os.Getenv("SMARTPIPE_FAKE_APPSERVER") == "1" {
		fakeAppServer()
		return
	}
	os.Exit(m.Run())
}

func fakeAppServer() {
	addr := os.Getenv("SMARTPIPE_FAKE_ADDR")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","version":"fake-1.0"}`))
	})
	fmt.Println("fake app server listening on", addr)
	srv := &http.Server{Addr: addr, Handler: mux}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Println("fake app server error:", err)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func fakeSpec(t *testing.T) Spec {
	t.Helper()
	addr := freeAddr(t)
	return Spec{
		Name:   "fake-" + strings.ReplaceAll(addr, ":", "-"),
		Binary: os.Args[0],
		Addr:   addr,
		Token:  "test-token",
		Dirs:   Dirs{LogDir: t.TempDir()},
		Args:   []string{"-test.run=^$"},
		Env:    []string{"SMARTPIPE_FAKE_APPSERVER=1", "SMARTPIPE_FAKE_ADDR=" + addr},
	}
}

func TestStartHealthStop(t *testing.T) {
	m := NewManager()
	spec := fakeSpec(t)
	inst, err := m.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if inst.Status != "running" || inst.PID <= 0 {
		t.Fatalf("unexpected instance: %+v", inst)
	}
	if inst.Health != "ok" {
		t.Fatalf("health = %q, want ok", inst.Health)
	}
	if inst.Version != "fake-1.0" {
		t.Fatalf("version = %q", inst.Version)
	}

	logs, err := m.LogTail(inst.ID, 50)
	if err != nil {
		t.Fatalf("log tail: %v", err)
	}
	if !strings.Contains(logs, "fake app server listening") {
		t.Fatalf("log missing startup line: %q", logs)
	}

	if err := m.Stop(inst.ID, 2*time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	after, ok := m.Get(inst.ID)
	if !ok || after.Status != "stopped" {
		t.Fatalf("expected stopped, got %+v", after)
	}
	if !portFree(spec.Addr) {
		t.Fatalf("port %s still in use after stop", spec.Addr)
	}
	if err := m.Stop(inst.ID, time.Second); err != ErrNotRunning {
		t.Fatalf("second stop: expected ErrNotRunning, got %v", err)
	}
}

func TestDuplicateAndNotFound(t *testing.T) {
	m := NewManager()
	spec := fakeSpec(t)
	spec.ID = "fixed"
	if _, err := m.Start(context.Background(), spec); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = m.Stop("fixed", 2*time.Second) }()

	if _, err := m.Start(context.Background(), spec); err != ErrDuplicate {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}
	if _, err := m.Health(context.Background(), "missing"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, err := m.LogTail("missing", 10); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for logs, got %v", err)
	}
}

func TestRestartKeepsSpec(t *testing.T) {
	m := NewManager()
	spec := fakeSpec(t)
	inst, err := m.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	firstPID := inst.PID

	restarted, err := m.Restart(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	defer func() { _ = m.Stop(inst.ID, 2*time.Second) }()

	if restarted.PID == firstPID {
		t.Fatalf("pid unchanged after restart: %d", firstPID)
	}
	if restarted.Health != "ok" || restarted.Addr != spec.Addr {
		t.Fatalf("unexpected restarted instance: %+v", restarted)
	}
}

func TestStartRejectsMissingBinary(t *testing.T) {
	m := NewManager()
	_, err := m.Start(context.Background(), Spec{Binary: "Z:/definitely/not/here.exe", Addr: "127.0.0.1:1"})
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}
