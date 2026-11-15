package releasesvc

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"smartpipe/backend/internal/store"
)

// executorCommands 是构建机侧的命令通道：
//
//	GET  .../{id}/commands/next?wait_seconds=25   长轮询领取命令
//	POST .../{id}/commands/{commandID}/result     回报执行结果
func (h *HTTPHandler) executorCommands(w http.ResponseWriter, r *http.Request, executorID string, rest []string) {
	if len(rest) == 0 {
		http.NotFound(w, r)
		return
	}
	switch {
	case rest[0] == "next" && r.Method == http.MethodGet:
		h.nextExecutorCommand(w, r, executorID)
	case len(rest) == 2 && rest[1] == "result" && r.Method == http.MethodPost:
		h.completeExecutorCommand(w, r, executorID, rest[0])
	default:
		http.NotFound(w, r)
	}
}

func (h *HTTPHandler) nextExecutorCommand(w http.ResponseWriter, r *http.Request, executorID string) {
	wait := 25
	if raw := strings.TrimSpace(r.URL.Query().Get("wait_seconds")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			wait = parsed
		}
	}
	if wait < 0 {
		wait = 0
	}
	if wait > 30 {
		wait = 30
	}
	// 顺手清理过期命令；每次长轮询最多一条 UPDATE，开销可以忽略。
	if n, err := h.Store.ExpireExecutorCommands(r.Context()); err == nil && n > 0 {
		// 过期命令不会返回给执行器，这里只做记录。
		_ = n
	}
	deadline := time.Now().Add(time.Duration(wait) * time.Second)
	for {
		cmd, err := h.Store.ClaimExecutorCommand(r.Context(), executorID)
		if err == nil {
			writeJSON(w, http.StatusOK, cmd)
			return
		}
		if !errors.Is(err, store.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if time.Now().After(deadline) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (h *HTTPHandler) completeExecutorCommand(w http.ResponseWriter, r *http.Request, executorID, rawID string) {
	id, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid command id", http.StatusBadRequest)
		return
	}
	var in struct {
		ExitCode     int    `json:"exit_code"`
		Output       string `json:"output"`
		ErrorMessage string `json:"error_message"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	ok, err := h.Store.CompleteExecutorCommand(r.Context(), id, executorID, in.ExitCode, in.Output, strings.TrimSpace(in.ErrorMessage))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		w.WriteHeader(http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
