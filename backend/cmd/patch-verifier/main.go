// patch-verifier 是补丁门禁服务：在干净副本上校验 base_commit、路径与密钥，
// 应用补丁后执行仓库 smartpipe.yaml 声明的命令，返回可归档的证据 JSON。
//
// 环境变量：
//
//	SMARTPIPE_VERIFY_ADDR        监听地址，默认 :8095
//	SMARTPIPE_VERIFY_TOKEN       X-Verify-Token 校验令牌（为空则不校验，仅限本地）
//	SMARTPIPE_VERIFY_WORK_ROOT   临时工作目录根，默认系统临时目录
//	SMARTPIPE_VERIFY_CONCURRENCY 并发验证数，默认 2
//	SMARTPIPE_VERIFY_TIMEOUT     单次验证总超时，默认 30m
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"smartpipe/backend/internal/patch"
	"smartpipe/backend/internal/workspace"
)

type verifyRequest struct {
	Repo                string            `json:"repo"`
	IssueID             string            `json:"issue_id"`
	BaseCommit          string            `json:"base_commit"`
	Patch               string            `json:"patch"`
	TestLog             string            `json:"test_log"`
	Environment         map[string]string `json:"environment"`
	Author              string            `json:"author"`
	AllowMissingProfile bool              `json:"allow_missing_profile"`
	KeepWorkDir         bool              `json:"keep_work_dir"`
}

func main() {
	addr := strings.TrimSpace(os.Getenv("SMARTPIPE_VERIFY_ADDR"))
	if addr == "" {
		addr = ":8095"
	}
	token := strings.TrimSpace(os.Getenv("SMARTPIPE_VERIFY_TOKEN"))
	workRoot := strings.TrimSpace(os.Getenv("SMARTPIPE_VERIFY_WORK_ROOT"))
	concurrency := envInt("SMARTPIPE_VERIFY_CONCURRENCY", 2)
	totalTimeout := envDuration("SMARTPIPE_VERIFY_TIMEOUT", 30*time.Minute)

	verifier := &patch.Verifier{WorkRoot: workRoot, Limits: patch.DefaultLimits()}
	wsRoot := strings.TrimSpace(os.Getenv("SMARTPIPE_WORKSPACE_ROOT"))
	if wsRoot == "" {
		wsRoot = filepath.Join(os.TempDir(), "smartpipe-workspaces")
	}
	wsManager := &workspace.Manager{Root: wsRoot}
	sem := make(chan struct{}, concurrency)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok", "service": "patch-verifier", "concurrency": concurrency,
		})
	})
	mux.HandleFunc("/workspaces", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			items, err := wsManager.List()
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": items})
		case http.MethodPost:
			if token != "" && r.Header.Get("X-Verify-Token") != token {
				http.Error(w, "invalid verify token", http.StatusUnauthorized)
				return
			}
			var in struct {
				ID         string `json:"id"`
				IssueRef   string `json:"issue_ref"`
				Repo       string `json:"repo"`
				BaseCommit string `json:"base_commit"`
				Branch     string `json:"branch"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			id := strings.TrimSpace(in.ID)
			if id == "" && strings.TrimSpace(in.IssueRef) != "" {
				id = workspace.ShortID(in.IssueRef)
			}
			info, err := wsManager.Prepare(r.Context(), id, in.Repo, in.BaseCommit, in.Branch)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusCreated, info)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/workspaces/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/workspaces/")
		if id == "" || strings.Contains(id, "/") {
			http.Error(w, "invalid workspace id", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodGet:
			info, err := wsManager.Get(id)
			if err != nil {
				writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, info)
		case http.MethodDelete:
			if token != "" && r.Header.Get("X-Verify-Token") != token {
				http.Error(w, "invalid verify token", http.StatusUnauthorized)
				return
			}
			if err := wsManager.Cleanup(id); err != nil {
				writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/push", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if token != "" && r.Header.Get("X-Verify-Token") != token {
			http.Error(w, "invalid verify token", http.StatusUnauthorized)
			return
		}
		var in patch.PushRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		res, err := verifier.Push(r.Context(), in)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		status := http.StatusOK
		if !res.OK {
			status = http.StatusUnprocessableEntity
		}
		writeJSON(w, status, res)
	})
	mux.HandleFunc("/commit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if token != "" && r.Header.Get("X-Verify-Token") != token {
			http.Error(w, "invalid verify token", http.StatusUnauthorized)
			return
		}
		var in patch.MergeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&in); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		res, err := verifier.CommitAndMerge(r.Context(), in)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		status := http.StatusOK
		if !res.OK {
			status = http.StatusUnprocessableEntity
		}
		writeJSON(w, status, res)
	})
	mux.HandleFunc("/verify", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if token != "" && r.Header.Get("X-Verify-Token") != token {
			http.Error(w, "invalid verify token", http.StatusUnauthorized)
			return
		}
		var in verifyRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&in); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(in.Repo) == "" || strings.TrimSpace(in.Patch) == "" {
			http.Error(w, "repo and patch are required", http.StatusBadRequest)
			return
		}
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		case <-r.Context().Done():
			http.Error(w, "client canceled", http.StatusRequestTimeout)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), totalTimeout)
		defer cancel()
		bundle := patch.Bundle{
			IssueID: in.IssueID, Repo: in.Repo, BaseCommit: in.BaseCommit,
			Patch: in.Patch, TestLog: in.TestLog, Environment: in.Environment, Author: in.Author,
		}
		verifier.AllowMissingProfile = in.AllowMissingProfile
		report, err := verifier.Verify(ctx, bundle, in.Repo)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		status := http.StatusOK
		if !report.OK {
			status = http.StatusUnprocessableEntity
		}
		writeJSON(w, status, report)
	})

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("patch-verifier 监听 %s（并发 %d）", addr, concurrency)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("patch-verifier 退出: %v", err)
	}
}

func envInt(key string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
