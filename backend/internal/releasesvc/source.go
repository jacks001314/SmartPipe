package releasesvc

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"smartpipe/backend/internal/releasev2"
)

// serveSource 把一次运行固定提交上的仓库源码打成 tar.gz 交给执行器。
//
// 构建机常常在内网里，连不上 GitHub；执行器 clone 失败时改从这里取源码，
// 构建机只需要能访问 SmartPipe 自身。内容来自控制面维护的本地镜像，
// 因此和计划里解析出的 commit 完全一致。
func (h *HTTPHandler) serveSource(w http.ResponseWriter, r *http.Request, executorID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	runID := strings.TrimSpace(r.URL.Query().Get("run_id"))
	if runID == "" {
		http.Error(w, "run_id is required", http.StatusBadRequest)
		return
	}
	run, err := h.Store.GetRun(r.Context(), runID)
	if err != nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	commit := strings.TrimSpace(r.URL.Query().Get("commit"))
	if commit == "" {
		commit = strings.TrimSpace(run.ResolvedCommit)
	}
	if commit == "" || commit != strings.TrimSpace(run.ResolvedCommit) {
		http.Error(w, "commit does not match the run", http.StatusBadRequest)
		return
	}
	project, err := h.Store.GetProject(r.Context(), run.ProjectID)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	dir := strings.TrimSpace(project.LocalRepoPath)
	if dir == "" {
		http.Error(w, "project has no local repository mirror", http.StatusConflict)
		return
	}
	if st, statErr := os.Stat(filepath.Join(dir, ".git")); statErr != nil || !st.IsDir() {
		http.Error(w, "local repository mirror is missing", http.StatusConflict)
		return
	}
	path, cleanup, err := archiveCommit(r.Context(), dir, strings.TrimSpace(project.GitHubRepoURL), commit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer cleanup()
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = h.Store.AppendEvent(r.Context(), releasev2.Event{
		RunID: run.ID, Actor: "executor:" + executorID, Kind: "source.served",
		Summary: "executor fetched source archive",
		Detail:  map[string]any{"commit": commit, "bytes": info.Size()},
	})
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("X-Commit-SHA", commit)
	http.ServeContent(w, r, "source.tar.gz", info.ModTime(), f)
}

// archiveCommit 在临时目录里生成该提交的归档文件；本地镜像里没有这个提交时先 fetch 一次。
func archiveCommit(parent context.Context, dir, remote, commit string) (string, func(), error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	hasCommit := func() bool {
		cmd := exec.CommandContext(ctx, "git", "-C", dir, "cat-file", "-e", commit+"^{commit}")
		return cmd.Run() == nil
	}
	if !hasCommit() {
		if remote != "" {
			_ = exec.CommandContext(ctx, "git", "-C", dir, "fetch", "--no-prune", remote, "+refs/heads/*:refs/remotes/origin/*").Run()
		}
		if !hasCommit() {
			cancel()
			return "", func() {}, fmt.Errorf("本地镜像里没有提交 %s，请先在仓库页同步", commit)
		}
	}
	tmp, err := os.CreateTemp("", "smartpipe-source-*.tar.gz")
	if err != nil {
		cancel()
		return "", func() {}, err
	}
	name := tmp.Name()
	_ = tmp.Close()
	cleanup := func() {
		cancel()
		_ = os.Remove(name)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "archive", "--format=tar.gz", "-o", name, commit)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		cleanup()
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", func() {}, fmt.Errorf("git archive %s: %s", commit, msg)
	}
	return name, cleanup, nil
}
