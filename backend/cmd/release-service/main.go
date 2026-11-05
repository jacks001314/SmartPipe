// release-service 从干净提交构建制品并生成带校验和的发版清单。
//
// 环境变量：
//
//	SMARTPIPE_RELEASE_ADDR         监听地址，默认 :8096
//	SMARTPIPE_RELEASE_TOKEN        X-Release-Token 校验令牌
//	SMARTPIPE_RELEASE_WORK_ROOT    临时克隆根目录
//	SMARTPIPE_RELEASE_OUTPUT_ROOT  制品输出目录
//	SMARTPIPE_RELEASE_SIGNING_KEY  manifest 的 HMAC 签名密钥（可选）
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"smartpipe/backend/internal/release"
)

func main() {
	addr := envOr("SMARTPIPE_RELEASE_ADDR", ":8096")
	token := strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_TOKEN"))
	workRoot := strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_WORK_ROOT"))
	outputRoot := strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_OUTPUT_ROOT"))
	signingKey := strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_SIGNING_KEY"))

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "release-service"})
	})
	mux.HandleFunc("/build", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if token != "" && r.Header.Get("X-Release-Token") != token {
			http.Error(w, "invalid release token", http.StatusUnauthorized)
			return
		}
		var req release.Request
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if req.OutputRoot == "" {
			req.OutputRoot = outputRoot
		}
		if req.SigningKey == "" {
			req.SigningKey = signingKey
		}
		report, err := release.Build(r.Context(), req, workRoot)
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
	log.Printf("release-service 监听 %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("release-service 退出: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
