// Package release 从干净提交构建制品：检出指定 commit、执行 smartpipe.yaml 的 build 命令、
// 收集制品并生成带 sha256 与可选 HMAC 签名的 manifest。
package release

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"smartpipe/backend/internal/patch"
)

// ErrInvalidRequest 表示发版参数不完整或不安全。
var ErrInvalidRequest = errors.New("release: invalid request")

var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var commitPattern = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// Request 是一次发版构建的输入。
type Request struct {
	Repo          string            `json:"repo"`
	Commit        string            `json:"commit"`
	Version       string            `json:"version"`
	ProfileName   string            `json:"profile_name"`
	ArtifactGlobs []string          `json:"artifact_globs"`
	OutputRoot    string            `json:"output_root"`
	SigningKey    string            `json:"signing_key"`
	Notes         string            `json:"notes"`
	Environment   map[string]string `json:"environment"`
}

// Artifact 是单个制品文件。
type Artifact struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest 是发版清单。
type Manifest struct {
	Version     string            `json:"version"`
	Commit      string            `json:"commit"`
	BuiltAt     time.Time         `json:"built_at"`
	Command     string            `json:"build_command"`
	Notes       string            `json:"notes,omitempty"`
	Artifacts   []Artifact        `json:"artifacts"`
	Environment map[string]string `json:"environment,omitempty"`
}

// Report 是一次构建的完整结果。
type Report struct {
	OK             bool       `json:"ok"`
	Version        string     `json:"version"`
	Commit         string     `json:"commit"`
	OutputDir      string     `json:"output_dir,omitempty"`
	Command        string     `json:"build_command,omitempty"`
	ExitCode       int        `json:"exit_code"`
	BuildOutput    string     `json:"build_output,omitempty"`
	Artifacts      []Artifact `json:"artifacts,omitempty"`
	Manifest       *Manifest  `json:"manifest,omitempty"`
	ManifestSHA256 string     `json:"manifest_sha256,omitempty"`
	Signature      string     `json:"signature,omitempty"`
	DurationMS     int64      `json:"duration_ms"`
	Error          string     `json:"error,omitempty"`
}

func (r *Request) normalize() error {
	r.Repo = strings.TrimSpace(r.Repo)
	r.Commit = strings.TrimSpace(r.Commit)
	r.Version = strings.TrimSpace(r.Version)
	if r.Repo == "" {
		return fmt.Errorf("%w: repo is required", ErrInvalidRequest)
	}
	if !commitPattern.MatchString(r.Commit) {
		return fmt.Errorf("%w: commit must be a git commit", ErrInvalidRequest)
	}
	if !versionPattern.MatchString(r.Version) {
		return fmt.Errorf("%w: unsafe version %q", ErrInvalidRequest, r.Version)
	}
	if r.ProfileName == "" {
		r.ProfileName = "smartpipe.yaml"
	}
	if len(r.ArtifactGlobs) == 0 {
		r.ArtifactGlobs = []string{"dist/**", "bin/**", "build/**"}
	}
	return nil
}

// Build 执行一次干净构建并返回报告。
func Build(ctx context.Context, req Request, workRoot string) (*Report, error) {
	started := time.Now()
	if strings.TrimSpace(workRoot) == "" {
		workRoot = os.TempDir()
	}
	rep := &Report{Version: req.Version, Commit: req.Commit, ExitCode: -1}
	if err := req.normalize(); err != nil {
		rep.Error = err.Error()
		rep.DurationMS = time.Since(started).Milliseconds()
		return rep, nil
	}
	rep.Version, rep.Commit = req.Version, req.Commit
	outputRoot := strings.TrimSpace(req.OutputRoot)
	if outputRoot == "" {
		outputRoot = filepath.Join(os.TempDir(), "smartpipe-releases")
	}
	if err := os.MkdirAll(outputRoot, 0o755); err != nil {
		return rep, fmt.Errorf("release: create output root: %w", err)
	}
	if err := os.MkdirAll(workRoot, 0o755); err != nil {
		return rep, fmt.Errorf("release: create work root: %w", err)
	}
	tmp, err := os.MkdirTemp(workRoot, "smartpipe-release-*")
	if err != nil {
		return rep, err
	}
	defer os.RemoveAll(tmp)
	repoDir := filepath.Join(tmp, "repo")

	if _, err := runGit(ctx, "", "clone", "--no-hardlinks", "--quiet", req.Repo, repoDir); err != nil {
		rep.Error = "clone failed: " + err.Error()
		rep.DurationMS = time.Since(started).Milliseconds()
		return rep, nil
	}
	if _, err := runGit(ctx, repoDir, "checkout", "--detach", req.Commit); err != nil {
		rep.Error = "checkout failed: " + err.Error()
		rep.DurationMS = time.Since(started).Milliseconds()
		return rep, nil
	}
	head, err := runGit(ctx, repoDir, "rev-parse", "HEAD")
	if err != nil {
		rep.Error = "rev-parse failed: " + err.Error()
		return rep, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(head), req.Commit) {
		rep.Error = fmt.Sprintf("commit mismatch: requested %s, checked out %s", req.Commit, strings.TrimSpace(head))
		rep.DurationMS = time.Since(started).Milliseconds()
		return rep, nil
	}
	rep.Commit = strings.TrimSpace(head)

	profile, err := patch.LoadProfile(filepath.Join(repoDir, req.ProfileName))
	if err != nil {
		rep.Error = err.Error()
		rep.DurationMS = time.Since(started).Milliseconds()
		return rep, nil
	}
	command := strings.TrimSpace(profile.Commands["build"])
	if command == "" {
		rep.Error = "profile has no build command"
		rep.DurationMS = time.Since(started).Milliseconds()
		return rep, nil
	}
	rep.Command = command
	out, code, timedOut := runShell(ctx, repoDir, command, profile.TimeoutFor("build"))
	rep.ExitCode = code
	rep.BuildOutput = truncate(out, 20000)
	if timedOut {
		rep.Error = "build command timed out"
		rep.DurationMS = time.Since(started).Milliseconds()
		return rep, nil
	}
	if code != 0 {
		rep.Error = "build command failed"
		rep.DurationMS = time.Since(started).Milliseconds()
		return rep, nil
	}

	artifacts, err := collectArtifacts(repoDir, req.ArtifactGlobs, outputRoot, req.Version)
	if err != nil {
		rep.Error = "collect artifacts: " + err.Error()
		rep.DurationMS = time.Since(started).Milliseconds()
		return rep, nil
	}
	rep.Artifacts = artifacts
	manifest := &Manifest{
		Version: req.Version, Commit: rep.Commit, BuiltAt: time.Now().UTC(),
		Command: command, Notes: req.Notes, Artifacts: artifacts, Environment: req.Environment,
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return rep, err
	}
	outDir := filepath.Join(outputRoot, req.Version)
	manifestPath := filepath.Join(outDir, "manifest.json")
	if err := os.WriteFile(manifestPath, raw, 0o644); err != nil {
		return rep, err
	}
	sum := sha256.Sum256(raw)
	rep.Manifest = manifest
	rep.ManifestSHA256 = "sha256:" + hex.EncodeToString(sum[:])
	rep.OutputDir = outDir
	if strings.TrimSpace(req.SigningKey) != "" {
		mac := hmac.New(sha256.New, []byte(req.SigningKey))
		mac.Write(raw)
		rep.Signature = "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
	}
	rep.OK = true
	rep.ExitCode = 0
	rep.DurationMS = time.Since(started).Milliseconds()
	return rep, nil
}

func collectArtifacts(repoDir string, globs []string, outputRoot, version string) ([]Artifact, error) {
	outDir := filepath.Join(outputRoot, version)
	var (
		files []string
		seen  = map[string]bool{}
	)
	for _, glob := range globs {
		matches, err := expandGlob(repoDir, glob)
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			rel, err := filepath.Rel(repoDir, m)
			if err != nil || strings.HasPrefix(rel, "..") {
				continue
			}
			rel = filepath.ToSlash(rel)
			if seen[rel] {
				continue
			}
			seen[rel] = true
			files = append(files, rel)
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no artifacts matched %v", globs)
	}
	artifacts := make([]Artifact, 0, len(files))
	for _, rel := range files {
		src := filepath.Join(repoDir, filepath.FromSlash(rel))
		dst := filepath.Join(outDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		sum, size, err := copyAndHash(src, dst)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, Artifact{Path: rel, Size: size, SHA256: sum})
	}
	return artifacts, nil
}

func expandGlob(root, glob string) ([]string, error) {
	glob = filepath.ToSlash(glob)
	if strings.HasSuffix(glob, "/**") {
		dir := filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(glob, "/**")))
		var out []string
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if !info.IsDir() {
				out = append(out, path)
			}
			return nil
		})
		return out, err
	}
	matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(glob)))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		info, err := os.Stat(m)
		if err == nil && !info.IsDir() {
			out = append(out, m)
		}
	}
	return out, nil
}

func copyAndHash(src, dst string) (string, int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return "", 0, err
	}
	defer out.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), in)
	if err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), n, nil
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-c", "core.autocrlf=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func runShell(ctx context.Context, dir, command string, timeout time.Duration) (string, int, bool) {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(runCtx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(runCtx, "/bin/sh", "-c", command)
	}
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CI=1", "SMARTPIPE_RELEASE=1")
	out, err := cmd.CombinedOutput()
	if runCtx.Err() == context.DeadlineExceeded {
		return string(out), -1, true
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return string(out), exitErr.ExitCode(), false
		}
		return string(out) + "\n" + err.Error(), -1, false
	}
	return string(out), 0, false
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n...[truncated]"
}
