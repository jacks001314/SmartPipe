package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"smartpipe/backend/internal/testlab"
	"strings"
	"testing"
)

func sessionSpec(projectID string, required ...string) testlab.SessionSpec {
	return testlab.SessionSpec{ProjectID: projectID, Title: "story", Kind: "story", Environment: "syntropy",
		Platform: "windows-amd64", Story: "# story", TimeoutSeconds: 600, RequiredChecks: required}
}

func TestTestSessionLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, &Project{Name: newID("session-test")})
	if err != nil {
		t.Fatal(err)
	}
	machine := newID("sm")
	token := strings.Repeat("t", 64)
	t.Cleanup(func() {
		_, _ = s.db.Exec("DELETE FROM test_sessions WHERE project_id=$1", p.ID)
		_, _ = s.db.Exec("DELETE FROM desktop_test_runs WHERE project_id=$1", p.ID)
		_, _ = s.db.Exec("DELETE FROM desktop_test_machines WHERE id=$1", machine)
	})
	if err = s.RegisterDesktopMachine(ctx, machine, "windows-amd64", testlab.TokenDigest(strings.Repeat("m", 64))); err != nil {
		t.Fatal(err)
	}
	id := newID("session")
	if _, err = s.CreateTestSession(ctx, id, sessionSpec(p.ID, "fixed"), testlab.TokenDigest(token)); err != nil {
		t.Fatal(err)
	}
	if err = s.AuthenticateTestSession(ctx, id, token); err != nil {
		t.Fatal(err)
	}
	lease, err := s.ClaimTestSession(ctx, machine, "syntropy")
	if err != nil {
		t.Fatal(err)
	}
	if lease.SessionID != id || lease.Session.Status != "RUNNING" || lease.Deadline.IsZero() {
		t.Fatalf("unexpected lease: %+v", lease)
	}
	if _, err = s.ClaimTestSession(ctx, machine, "syntropy"); !errors.Is(err, testlab.ErrState) {
		t.Fatalf("busy machine claimed again: %v", err)
	}
	runSpec := testlab.Spec{ProjectID: p.ID, SourceSHA: strings.Repeat("a", 40), ArtifactSHA256: strings.Repeat("b", 64),
		Scenario: "syntropy-desktop-hotswap", ScenarioVersion: "1", Platform: "windows-amd64", RequiredChecks: []string{"app_window"}}
	if _, err = s.CreateDesktopTestRun(ctx, newID("run"), runSpec); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimDesktopRun(ctx, machine, "syntropy-desktop-hotswap@1"); !errors.Is(err, testlab.ErrState) {
		t.Fatalf("run claimed a session-busy machine: %v", err)
	}
	command, err := s.EnqueueTestSessionCommand(ctx, id, "read_file", json.RawMessage(`{"path":"C:\\x"}`))
	if err != nil {
		t.Fatal(err)
	}
	taken, err := s.TakeTestSessionCommand(ctx, machine, id, lease.Generation)
	if err != nil || taken.ID != command.ID {
		t.Fatalf("take: %v %+v", err, taken)
	}
	if _, err = s.TakeTestSessionCommand(ctx, machine, id, lease.Generation); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected empty queue: %v", err)
	}
	if err = s.CompleteTestSessionCommand(ctx, machine, id, lease.Generation, command.ID, "DONE", json.RawMessage(`{"content":"x"}`)); err != nil {
		t.Fatal(err)
	}
	payload := []byte("screenshot")
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	ref, err := s.PutTestSessionMachineEvidence(ctx, machine, id, lease.Generation,
		DesktopEvidence{SHA256: digest, MIMEType: "image/png", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordTestSessionCheck(ctx, id, testlab.SessionCheck{ID: "fixed", Status: "PASS", Evidence: ref}); err != nil {
		t.Fatal(err)
	}
	if err = s.RequestTestSessionFinish(ctx, id, "PASS", "verified"); err != nil {
		t.Fatal(err)
	}
	finish, err := s.TakeTestSessionCommand(ctx, machine, id, lease.Generation)
	if err != nil || finish.Kind != "finish" {
		t.Fatalf("finish command: %v %+v", err, finish)
	}
	done, err := s.FinishTestSession(ctx, machine, id, lease.Generation, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "PASS" || done.Verdict != "PASS" || done.Summary != "verified" {
		t.Fatalf("unexpected session: %+v", done)
	}
	if err = s.ReleaseTestSession(ctx, machine, id, lease.Generation); err != nil {
		t.Fatal(err)
	}
	var sessionID string
	if err = s.db.QueryRowContext(ctx, `SELECT session_id FROM desktop_test_machines WHERE id=$1`, machine).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	if sessionID != "" {
		t.Fatal("machine still leased after release")
	}
}

func TestTestSessionRequiresEvidence(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, &Project{Name: newID("session-evidence")})
	if err != nil {
		t.Fatal(err)
	}
	machine := newID("sm2")
	t.Cleanup(func() {
		_, _ = s.db.Exec("DELETE FROM test_sessions WHERE project_id=$1", p.ID)
		_, _ = s.db.Exec("DELETE FROM desktop_test_machines WHERE id=$1", machine)
	})
	if err = s.RegisterDesktopMachine(ctx, machine, "windows-amd64", testlab.TokenDigest(strings.Repeat("n", 64))); err != nil {
		t.Fatal(err)
	}
	id := newID("session")
	if _, err = s.CreateTestSession(ctx, id, sessionSpec(p.ID, "fixed"), testlab.TokenDigest(strings.Repeat("u", 64))); err != nil {
		t.Fatal(err)
	}
	lease, err := s.ClaimTestSession(ctx, machine, "syntropy")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordTestSessionCheck(ctx, id, testlab.SessionCheck{ID: "fixed", Status: "PASS"}); err != nil {
		t.Fatal(err)
	}
	done, err := s.FinishTestSession(ctx, machine, id, lease.Generation, "PASS", "no evidence")
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "BLOCKED" {
		t.Fatalf("expected BLOCKED, got %s", done.Status)
	}
}

func TestTestSessionCancelTerminatesPolling(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, &Project{Name: newID("session-cancel")})
	if err != nil {
		t.Fatal(err)
	}
	machine := newID("sm3")
	t.Cleanup(func() {
		_, _ = s.db.Exec("DELETE FROM test_sessions WHERE project_id=$1", p.ID)
		_, _ = s.db.Exec("DELETE FROM desktop_test_machines WHERE id=$1", machine)
	})
	if err = s.RegisterDesktopMachine(ctx, machine, "windows-amd64", testlab.TokenDigest(strings.Repeat("o", 64))); err != nil {
		t.Fatal(err)
	}
	id := newID("session")
	if _, err = s.CreateTestSession(ctx, id, sessionSpec(p.ID), testlab.TokenDigest(strings.Repeat("v", 64))); err != nil {
		t.Fatal(err)
	}
	lease, err := s.ClaimTestSession(ctx, machine, "syntropy")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CancelTestSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.TakeTestSessionCommand(ctx, machine, id, lease.Generation); !errors.Is(err, ErrSessionTerminal) {
		t.Fatalf("expected terminal, got %v", err)
	}
	if err = s.ReleaseTestSession(ctx, machine, id, lease.Generation); err != nil {
		t.Fatal(err)
	}
}
