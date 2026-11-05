package release

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T, buildCmd string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "dev@example.com")
	git(t, dir, "config", "user.name", "dev")
	profile := "version: 1\ncommands:\n  build: \"" + buildCmd + "\"\nrequired_for_merge: [build]\ntimeouts:\n  build: 60s\n"
	if err := os.WriteFile(filepath.Join(dir, "smartpipe.yaml"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	return dir, git(t, dir, "rev-parse", "HEAD")
}

func TestBuildProducesManifestAndChecksum(t *testing.T) {
	repo, commit := newRepo(t, "git --version > dist.txt")
	outRoot := filepath.Join(t.TempDir(), "releases")
	report, err := Build(context.Background(), Request{
		Repo: repo, Commit: commit, Version: "v0.1.0", OutputRoot: outRoot,
		ArtifactGlobs: []string{"dist.txt"}, SigningKey: "test-key",
	}, t.TempDir())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !report.OK || len(report.Artifacts) != 1 || report.Signature == "" {
		t.Fatalf("report = %+v", report)
	}
	if !strings.HasPrefix(report.Artifacts[0].SHA256, "sha256:") {
		t.Fatalf("artifact = %+v", report.Artifacts[0])
	}
	if _, err := os.Stat(filepath.Join(outRoot, "v0.1.0", "manifest.json")); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	if report.Manifest.Commit != commit {
		t.Fatalf("manifest commit = %s", report.Manifest.Commit)
	}
}

func TestBuildFailingCommand(t *testing.T) {
	repo, commit := newRepo(t, "git --not-a-real-option > dist.txt")
	report, err := Build(context.Background(), Request{
		Repo: repo, Commit: commit, Version: "v0.2.0", ArtifactGlobs: []string{"dist.txt"},
	}, t.TempDir())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if report.OK || report.ExitCode == 0 || !strings.Contains(report.Error, "failed") {
		t.Fatalf("report = %+v", report)
	}
}

func TestBuildRejectsUnsafeVersion(t *testing.T) {
	repo, commit := newRepo(t, "git --version > dist.txt")
	report, err := Build(context.Background(), Request{Repo: repo, Commit: commit, Version: "../evil"}, t.TempDir())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if report.OK || !strings.Contains(report.Error, "unsafe version") {
		t.Fatalf("report = %+v", report)
	}
}
