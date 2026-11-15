package releasesvc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"smartpipe/backend/internal/releasev2"
)

// putTargetManifest 接收构建机的目标级制品清单，写入对象存储后尝试合并
// 出 run 级清单。清单是发布后续环节（灌库、渠道、GitHub）的唯一制品来源。
func (h *HTTPHandler) putTargetManifest(w http.ResponseWriter, r *http.Request, executorID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	objects := h.objectsFor(r.Context())
	if objects == nil {
		http.Error(w, "artifact storage is not configured", http.StatusServiceUnavailable)
		return
	}
	var in releasev2.ArtifactManifest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	in.RunID = strings.TrimSpace(in.RunID)
	in.TargetID = strings.TrimSpace(in.TargetID)
	if in.RunID == "" || in.TargetID == "" {
		http.Error(w, "run_id and target_id are required", http.StatusBadRequest)
		return
	}
	target, err := h.Store.GetTarget(r.Context(), in.RunID, in.TargetID)
	if err != nil {
		http.Error(w, "target not found", http.StatusNotFound)
		return
	}
	switch target.Status {
	case releasev2.TargetBuilding, releasev2.TargetVerifying, releasev2.TargetLeased, releasev2.TargetUploaded:
	default:
		http.Error(w, fmt.Sprintf("target is %s, not building", target.Status), http.StatusConflict)
		return
	}
	if err := in.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !strings.EqualFold(in.OS, target.OS) || !strings.EqualFold(in.Arch, target.Arch) {
		http.Error(w, fmt.Sprintf("manifest platform %s/%s does not match target %s/%s", in.OS, in.Arch, target.OS, target.Arch), http.StatusBadRequest)
		return
	}
	raw, err := json.Marshal(in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	key := "manifests/" + sanitizeKeyPart(in.RunID) + "/" + sanitizeKeyPart(in.TargetID) + ".json"
	if _, err := objects.Put(r.Context(), key, bytes.NewReader(raw), int64(len(raw)), "application/json"); err != nil {
		http.Error(w, "store artifact manifest: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.Store.SetTargetManifest(r.Context(), in.RunID, in.TargetID, key); err != nil {
		http.Error(w, "persist artifact manifest: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_ = h.Store.AppendEvent(r.Context(), releasev2.Event{
		RunID: in.RunID, Actor: "executor:" + executorID, Kind: "target.manifest", TargetID: in.TargetID,
		Summary: "artifact manifest uploaded",
		Detail:  map[string]any{"storage_key": key, "artifacts": len(in.Artifacts)},
	})
	if err := h.assembleRunManifest(r.Context(), in.RunID); err != nil {
		http.Error(w, "assemble run manifest: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"storage_key": key, "artifacts": len(in.Artifacts)})
}

// assembleRunManifest 在所有 target 都上报清单后合并出 run 级清单。
// 尚未集齐时直接返回，由最后一个上报的 target 触发。
func (h *HTTPHandler) assembleRunManifest(ctx context.Context, runID string) error {
	runID = strings.TrimSpace(runID)
	objects := h.objectsFor(ctx)
	if objects == nil {
		return fmt.Errorf("artifact storage is not configured")
	}
	run, err := h.Store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	targets, err := h.Store.ListTargets(ctx, runID)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}
	manifests := make([]releasev2.ArtifactManifest, 0, len(targets))
	for _, target := range targets {
		key := strings.TrimSpace(target.ManifestPath)
		if key == "" {
			return nil
		}
		obj, err := objects.Open(ctx, key)
		if err != nil {
			return fmt.Errorf("open target manifest %s: %w", key, err)
		}
		var manifest releasev2.ArtifactManifest
		decodeErr := json.NewDecoder(io.LimitReader(obj, 8<<20)).Decode(&manifest)
		obj.Close()
		if decodeErr != nil {
			return fmt.Errorf("decode target manifest %s: %w", key, decodeErr)
		}
		manifests = append(manifests, manifest)
	}
	merged, err := releasev2.MergeArtifactManifests(run, manifests)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	key := "manifests/" + sanitizeKeyPart(runID) + "/run.json"
	if _, err := objects.Put(ctx, key, bytes.NewReader(raw), int64(len(raw)), "application/json"); err != nil {
		return fmt.Errorf("store run manifest: %w", err)
	}
	if err := h.Store.SetRunManifest(ctx, runID, key); err != nil {
		return err
	}
	_ = h.Store.AppendEvent(ctx, releasev2.Event{
		RunID: runID, Actor: "release-service", Kind: "run.manifest",
		Summary: "run artifact manifest assembled",
		Detail:  map[string]any{"storage_key": key, "targets": len(manifests)},
	})
	return nil
}
