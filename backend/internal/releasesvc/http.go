package releasesvc

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"smartpipe/backend/internal/releasev2"
	"smartpipe/backend/internal/store"
)

type HTTPHandler struct {
	Store            ControlStore
	Objects          releasev2.ObjectStore
	Token            string
	MaxArtifactBytes int64
}

func (h *HTTPHandler) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "release-service"})
	})
	mux.HandleFunc("/internal/release-executors/register", h.auth(h.registerExecutor))
	mux.HandleFunc("/internal/release-executors/", h.auth(h.executorAction))
	return mux
}

func (h *HTTPHandler) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if h.Token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(h.Token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (h *HTTPHandler) registerExecutor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in releasev2.Executor
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	in.ID = strings.TrimSpace(in.ID)
	in.OS = strings.ToLower(strings.TrimSpace(in.OS))
	in.Arch = strings.ToLower(strings.TrimSpace(in.Arch))
	if in.ID == "" || in.OS == "" || in.Arch == "" {
		http.Error(w, "id, os and arch are required", http.StatusBadRequest)
		return
	}
	if in.Status == "" {
		in.Status = "online"
	}
	if in.Labels == nil {
		in.Labels = []string{}
	}
	if in.Capabilities == nil {
		in.Capabilities = map[string]any{}
	}
	if err := h.Store.UpsertExecutor(r.Context(), in); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

func (h *HTTPHandler) executorAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/internal/release-executors/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		http.Error(w, "invalid executor path", http.StatusBadRequest)
		return
	}
	executorID := parts[0]
	action := parts[1]
	switch action {
	case "claim":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		executor, err := h.Store.GetExecutor(r.Context(), executorID)
		if err == store.ErrNotFound {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		target, err := h.Store.ClaimReleaseV2Target(r.Context(), executorID, executor.OS, executor.Arch, 2*time.Minute)
		if err == store.ErrNotFound {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		run, err := h.Store.GetRun(r.Context(), target.RunID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = h.Store.AppendEvent(r.Context(), releasev2.Event{
			RunID: run.ID, Actor: "executor:" + executorID, Kind: "target.claimed", TargetID: target.TargetID,
			Summary: "target claimed by executor", Detail: map[string]any{"attempt": target.Attempt},
		})
		writeJSON(w, http.StatusOK, map[string]any{"target": target, "run": run})
	case "artifacts":
		if len(parts) < 3 {
			http.Error(w, "invalid artifact path", http.StatusBadRequest)
			return
		}
		switch parts[2] {
		case "begin":
			h.beginArtifact(w, r, executorID)
		case "put":
			h.putArtifact(w, r, executorID)
		case "confirm":
			h.confirmArtifact(w, r, executorID)
		default:
			http.NotFound(w, r)
		}
	case "heartbeat":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var in struct {
			RunID    string `json:"run_id"`
			TargetID string `json:"target_id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		ok, err := h.Store.HeartbeatReleaseV2Target(r.Context(), in.RunID, in.TargetID, executorID, 2*time.Minute)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "complete":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var in struct {
			RunID        string `json:"run_id"`
			TargetID     string `json:"target_id"`
			OK           bool   `json:"ok"`
			ErrorCode    string `json:"error_code"`
			ErrorMessage string `json:"error_message"`
			Log          string `json:"log"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&in); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		ok, err := h.Store.FinishReleaseV2Target(r.Context(), in.RunID, in.TargetID, executorID, in.OK, in.ErrorCode, in.ErrorMessage)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			w.WriteHeader(http.StatusConflict)
			return
		}
		_ = h.Store.AppendEvent(r.Context(), releasev2.Event{
			RunID: in.RunID, Actor: "executor:" + executorID, Kind: "target.finished", TargetID: in.TargetID,
			Summary: firstNonEmpty(in.ErrorMessage, "target completed"), Detail: map[string]any{"ok": in.OK, "log": truncate(in.Log, 64<<10)},
		})
		w.WriteHeader(http.StatusNoContent)
	case "log":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var in struct {
			RunID    string `json:"run_id"`
			TargetID string `json:"target_id"`
			Chunk    string `json:"chunk"`
			Seq      int    `json:"seq"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if len(in.Chunk) > 32<<10 {
			http.Error(w, "log chunk too large", http.StatusRequestEntityTooLarge)
			return
		}
		if err := h.Store.AppendLogChunk(r.Context(), in.RunID, in.TargetID, in.Chunk, in.Seq, "executor:"+executorID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max] + "\n...[truncated]"
}
