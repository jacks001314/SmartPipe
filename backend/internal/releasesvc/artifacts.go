package releasesvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"smartpipe/backend/internal/releasev2"
)

// ControlStore is the subset of the durable store used by the executor API.
type ControlStore interface {
	GetExecutor(context.Context, string) (*releasev2.Executor, error)
	UpsertExecutor(context.Context, releasev2.Executor) error
	ClaimReleaseV2Target(context.Context, string, string, string, time.Duration) (*releasev2.Target, error)
	GetRun(context.Context, string) (*releasev2.Run, error)
	GetTarget(context.Context, string, string) (*releasev2.Target, error)
	HeartbeatReleaseV2Target(context.Context, string, string, string, time.Duration) (bool, error)
	FinishReleaseV2Target(context.Context, string, string, string, bool, string, string) (bool, error)
	RequeueReleaseV2TargetLease(context.Context, string, string, string, string, string, int) (bool, error)
	MarkTargetStatus(context.Context, string, string, releasev2.TargetStatus, releasev2.TargetStatus) (bool, error)
	AppendEvent(context.Context, releasev2.Event) error
	AppendLogChunk(context.Context, string, string, string, int, string) error
	AddArtifact(context.Context, releasev2.Artifact) (*releasev2.Artifact, error)
}

type artifactBeginRequest struct {
	RunID       string `json:"run_id"`
	TargetID    string `json:"target_id"`
	Attempt     int    `json:"attempt"`
	Role        string `json:"role"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
	SHA256      string `json:"sha256"`
}

type artifactBeginResponse struct {
	UploadURL   string            `json:"upload_url"`
	Method      string            `json:"method"`
	Headers     map[string]string `json:"headers,omitempty"`
	StorageKey  string            `json:"storage_key"`
	ExpiresIn   int               `json:"expires_in_seconds"`
	ContentType string            `json:"content_type"`
	Direct      bool              `json:"direct"`
}

func (h *HTTPHandler) artifactStorageKey(in artifactBeginRequest) (string, error) {
	name := path.Base(strings.TrimSpace(in.Name))
	if name == "" || name == "." || name == "/" || strings.Contains(name, "..") {
		return "", fmt.Errorf("invalid artifact name %q", in.Name)
	}
	if strings.TrimSpace(in.TargetID) == "" || strings.TrimSpace(in.Role) == "" {
		return "", fmt.Errorf("target_id and role are required")
	}
	attempt := in.Attempt
	if attempt <= 0 {
		attempt = 1
	}
	return fmt.Sprintf("releases/%s/%s/%d/%s/%s", sanitizeKeyPart(in.RunID), sanitizeKeyPart(in.TargetID), attempt, sanitizeKeyPart(in.Role), sanitizeKeyPart(name)), nil
}

func sanitizeKeyPart(v string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(v) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

func (h *HTTPHandler) artifactLimit() int64 {
	if h.MaxArtifactBytes > 0 {
		return h.MaxArtifactBytes
	}
	return 8 << 30
}

func (h *HTTPHandler) targetForUpload(ctx context.Context, in artifactBeginRequest) (*releasev2.Target, error) {
	target, err := h.Store.GetTarget(ctx, in.RunID, in.TargetID)
	if err != nil {
		return nil, err
	}
	switch target.Status {
	case releasev2.TargetBuilding, releasev2.TargetVerifying, releasev2.TargetLeased:
		return target, nil
	default:
		return nil, fmt.Errorf("target is %s, not building", target.Status)
	}
}

func (h *HTTPHandler) validateArtifactRequest(target *releasev2.Target, in *artifactBeginRequest) error {
	if in.Size <= 0 || in.Size > h.artifactLimit() {
		return fmt.Errorf("artifact size is missing or too large")
	}
	for _, rule := range target.Artifacts {
		if rule.Role == strings.TrimSpace(in.Role) {
			if strings.TrimSpace(in.ContentType) == "" {
				in.ContentType = rule.ContentType
			}
			return nil
		}
	}
	return fmt.Errorf("role is not declared by the target profile")
}

// beginArtifact validates the target artifact rule and returns either a
// pre-signed PUT URL or a relay URL served by this process.
func (h *HTTPHandler) beginArtifact(w http.ResponseWriter, r *http.Request, executorID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	objects := h.objectsFor(r.Context())
	if objects == nil {
		http.Error(w, "artifact storage is not configured", http.StatusServiceUnavailable)
		return
	}
	var in artifactBeginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	target, err := h.targetForUpload(r.Context(), in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err := h.validateArtifactRequest(target, &in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	key, err := h.artifactStorageKey(in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if presigner, ok := h.objectsFor(r.Context()).(releasev2.PutPresigner); ok {
		uploadURL, headers, err := presigner.PresignPut(r.Context(), key, in.Size, in.ContentType, 30*time.Minute)
		if err == nil && uploadURL != "" {
			writeJSON(w, http.StatusOK, artifactBeginResponse{
				UploadURL: uploadURL, Method: http.MethodPut, Headers: headers, StorageKey: key,
				ExpiresIn: 1800, ContentType: in.ContentType, Direct: true,
			})
			return
		}
	}
	query := url.Values{}
	query.Set("run_id", in.RunID)
	query.Set("target_id", in.TargetID)
	query.Set("attempt", strconv.Itoa(maxInt(in.Attempt, 1)))
	query.Set("role", in.Role)
	query.Set("name", in.Name)
	query.Set("size", strconv.FormatInt(in.Size, 10))
	query.Set("content_type", in.ContentType)
	relay := "/internal/release-executors/" + url.PathEscape(executorID) + "/artifacts/put?" + query.Encode()
	writeJSON(w, http.StatusOK, artifactBeginResponse{
		UploadURL: relay, Method: http.MethodPut, StorageKey: key, ExpiresIn: 1800, ContentType: in.ContentType,
	})
}

// putArtifact streams the request body into object storage (relay mode),
// verifies size/digest and registers the artifact row.
func (h *HTTPHandler) putArtifact(w http.ResponseWriter, r *http.Request, executorID string) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	objects := h.objectsFor(r.Context())
	if objects == nil {
		http.Error(w, "artifact storage is not configured", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	in := artifactBeginRequest{
		RunID: q.Get("run_id"), TargetID: q.Get("target_id"), Role: q.Get("role"),
		Name: q.Get("name"), ContentType: q.Get("content_type"),
	}
	in.Attempt, _ = strconv.Atoi(q.Get("attempt"))
	in.Size, _ = strconv.ParseInt(q.Get("size"), 10, 64)
	target, err := h.targetForUpload(r.Context(), in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if in.Size <= 0 || in.Size > h.artifactLimit() {
		http.Error(w, "invalid artifact size", http.StatusBadRequest)
		return
	}
	key, err := h.artifactStorageKey(in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	stored, err := objects.Put(r.Context(), key, r.Body, in.Size, in.ContentType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	artifact, err := h.registerArtifact(r.Context(), executorID, in, stored, target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusCreated, artifact)
}

// confirmArtifact registers a direct (pre-signed) upload after re-hashing the
// stored object server side.
func (h *HTTPHandler) confirmArtifact(w http.ResponseWriter, r *http.Request, executorID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	objects := h.objectsFor(r.Context())
	if objects == nil {
		http.Error(w, "artifact storage is not configured", http.StatusServiceUnavailable)
		return
	}
	var in artifactBeginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	target, err := h.targetForUpload(r.Context(), in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if in.Size <= 0 {
		http.Error(w, "artifact size is required", http.StatusBadRequest)
		return
	}
	key, err := h.artifactStorageKey(in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	stored, err := objects.Stat(r.Context(), key)
	if err != nil {
		http.Error(w, "uploaded object not found: "+err.Error(), http.StatusNotFound)
		return
	}
	if stored.Size != in.Size {
		http.Error(w, fmt.Sprintf("stored size %d != declared %d", stored.Size, in.Size), http.StatusConflict)
		return
	}
	if strings.TrimSpace(stored.SHA256) == "" {
		digest, size, err := hashObject(r.Context(), objects, key)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		stored.SHA256, stored.Size = digest, size
	}
	if want := strings.TrimSpace(in.SHA256); want != "" && want != stored.SHA256 {
		http.Error(w, fmt.Sprintf("stored digest %s != uploaded %s", stored.SHA256, want), http.StatusConflict)
		return
	}
	stored.Key = key
	artifact, err := h.registerArtifact(r.Context(), executorID, in, stored, target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusCreated, artifact)
}

// artifactSigned 判断某个角色是否被签名：目标配了 sign 命令，且该角色的 signed 不是 false。
func artifactSigned(target *releasev2.Target, role string) bool {
	if target == nil || strings.TrimSpace(target.SignCommand) == "" {
		return false
	}
	role = strings.TrimSpace(role)
	for _, rule := range target.Artifacts {
		if strings.TrimSpace(rule.Role) != role || rule.Signed == nil {
			continue
		}
		return *rule.Signed
	}
	return true
}

func (h *HTTPHandler) registerArtifact(ctx context.Context, executorID string, in artifactBeginRequest, stored releasev2.StoredObject, target *releasev2.Target) (*releasev2.Artifact, error) {
	attempt := in.Attempt
	if attempt <= 0 {
		attempt = target.Attempt
	}
	// 目标配置了签名命令、并且签名步骤已经跑过（构建机按 build→sign→verify 顺序执行），
	// 这一轮上传的制品就登记为平台签名产物；制品规则里 signed=false 的角色例外。
	platformSigned := artifactSigned(target, in.Role)
	artifact, err := h.Store.AddArtifact(ctx, releasev2.Artifact{
		RunID: in.RunID, TargetID: in.TargetID, Attempt: attempt, Role: strings.TrimSpace(in.Role),
		Name: path.Base(in.Name), ContentType: in.ContentType, Size: stored.Size, SHA256: stored.SHA256,
		StorageKey: stored.Key, PlatformSigned: platformSigned,
	})
	if err != nil {
		return nil, err
	}
	_, _ = h.Store.MarkTargetStatus(ctx, in.RunID, in.TargetID, releasev2.TargetBuilding, releasev2.TargetUploaded)
	_, _ = h.Store.MarkTargetStatus(ctx, in.RunID, in.TargetID, releasev2.TargetVerifying, releasev2.TargetUploaded)
	_ = h.Store.AppendEvent(ctx, releasev2.Event{
		RunID: in.RunID, Actor: "executor:" + executorID, Kind: "artifact.uploaded", TargetID: in.TargetID,
		Summary: artifact.Name,
		Detail:  map[string]any{"role": artifact.Role, "size": artifact.Size, "sha256": artifact.SHA256, "storage_key": artifact.StorageKey},
	})
	return artifact, nil
}

func hashObject(ctx context.Context, objects releasev2.ObjectStore, key string) (string, int64, error) {
	reader, err := objects.Open(ctx, key)
	if err != nil {
		return "", 0, err
	}
	defer reader.Close()
	h := sha256.New()
	size, err := io.Copy(h, reader)
	if err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), size, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
