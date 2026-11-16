package releasesvc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"smartpipe/backend/internal/releasev2"
)

// maxSourceArchiveBytes 限制单次源码归档大小，避免误传大文件占满对象存储。
const maxSourceArchiveBytes int64 = 2 << 30

// releaseRunAction 是控制面内部接口：当前只有源码冻结。
func (h *HTTPHandler) releaseRunAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/internal/release-runs/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[1] != "source" {
		http.NotFound(w, r)
		return
	}
	h.freezeSource(w, r, parts[0])
}

// freezeSource 把一次运行的源码归档固化到对象存储。
//
// 调用方可以两种方式提供归档：
//   - POST body 为 tar.gz（admin-api 从 GitHub 下载）；
//   - ?from=mirror，由 release-service 从本地镜像按固定 commit 执行 git archive。
//
// 冻结之后执行器只读这个对象，运行内容不会随镜像更新而变化。
func (h *HTTPHandler) freezeSource(w http.ResponseWriter, r *http.Request, runID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	runID = strings.TrimSpace(runID)
	run, err := h.Store.GetRun(r.Context(), runID)
	if err != nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	if key := strings.TrimSpace(run.SourceStorageKey); key != "" && r.URL.Query().Get("force") != "1" {
		writeJSON(w, http.StatusOK, map[string]any{
			"storage_key": key, "sha256": run.SourceSHA256, "already_frozen": true,
		})
		return
	}
	if h.Objects == nil {
		http.Error(w, "object store is not configured", http.StatusServiceUnavailable)
		return
	}
	var (
		path    string
		cleanup = func() {}
		size    int64
	)
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("from")), "mirror") {
		project, perr := h.Store.GetProject(r.Context(), run.ProjectID)
		if perr != nil {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		dir := strings.TrimSpace(project.LocalRepoPath)
		if dir == "" {
			http.Error(w, "project has no local repository mirror", http.StatusConflict)
			return
		}
		path, cleanup, err = archiveCommit(r.Context(), dir, strings.TrimSpace(project.GitHubRepoURL), strings.TrimSpace(run.ResolvedCommit))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if info, statErr := os.Stat(path); statErr == nil {
			size = info.Size()
		}
	} else {
		path, size, err = stageSourceBody(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cleanup = func() { _ = os.Remove(path) }
	}
	defer cleanup()
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	key := "sources/" + runID + ".tar.gz"
	stored, err := h.Objects.Put(r.Context(), key, f, size, "application/gzip")
	if err != nil {
		http.Error(w, "store source archive: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.Store.SetRunSource(r.Context(), runID, stored.SHA256, stored.Key); err != nil {
		http.Error(w, "persist source archive: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_ = h.Store.AppendEvent(r.Context(), releasev2.Event{
		RunID: runID, Actor: "release-service", Kind: "source.frozen",
		Summary: "source archive frozen",
		Detail:  map[string]any{"commit": run.ResolvedCommit, "bytes": stored.Size, "sha256": stored.SHA256},
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"storage_key": stored.Key, "sha256": stored.SHA256, "size": stored.Size,
	})
}

// stageSourceBody 把上传流落到临时文件并返回大小；超过上限直接拒绝。
func stageSourceBody(r *http.Request) (string, int64, error) {
	tmp, err := os.CreateTemp("", "smartpipe-upload-*.tar.gz")
	if err != nil {
		return "", 0, err
	}
	name := tmp.Name()
	n, err := io.Copy(tmp, io.LimitReader(r.Body, maxSourceArchiveBytes+1))
	closeErr := tmp.Close()
	if err != nil {
		_ = os.Remove(name)
		return "", 0, err
	}
	if closeErr != nil {
		_ = os.Remove(name)
		return "", 0, closeErr
	}
	if n > maxSourceArchiveBytes {
		_ = os.Remove(name)
		return "", 0, fmt.Errorf("source archive exceeds %d bytes", maxSourceArchiveBytes)
	}
	if n == 0 {
		_ = os.Remove(name)
		return "", 0, fmt.Errorf("source archive is empty")
	}
	return name, n, nil
}

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
	if key := strings.TrimSpace(run.SourceStorageKey); key != "" {
		if h.Objects == nil {
			http.Error(w, "object store is not configured", http.StatusServiceUnavailable)
			return
		}
		obj, err := h.Objects.Open(r.Context(), key)
		if err != nil {
			http.Error(w, "open frozen source: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer obj.Close()
		size := int64(-1)
		if info, statErr := h.Objects.Stat(r.Context(), key); statErr == nil {
			size = info.Size
		}
		_ = h.Store.AppendEvent(r.Context(), releasev2.Event{
			RunID: run.ID, Actor: "executor:" + executorID, Kind: "source.served",
			Summary: "executor fetched frozen source archive",
			Detail:  map[string]any{"commit": commit, "bytes": size, "frozen": true},
		})
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("X-Commit-SHA", commit)
		w.Header().Set("X-Source-Frozen", "1")
		if size >= 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, obj)
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
	// 大仓库的 git fetch / archive 在慢磁盘或冷缓存上可能跑很久；固定 10 分钟
	// 会把健康的源码冻结砍断。取消交给调用方 context。
	ctx, cancel := context.WithCancel(parent)
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
