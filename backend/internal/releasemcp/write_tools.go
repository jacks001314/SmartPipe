package releasemcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"smartpipe/backend/internal/releasepublish"
	"smartpipe/backend/internal/releasev2"
)

const maxWaitBudget = 25 * time.Second

func (s *Server) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now().UTC()
}

func (s *Server) credentials() releasepublish.Provider {
	if s.Creds != nil {
		return s.Creds
	}
	return releasepublish.EnvProvider{Lookup: os.LookupEnv}
}

func (s *Server) updateClient(ctx context.Context) *releasepublish.UpdateClient {
	if s.Update != nil {
		return s.Update
	}
	base := firstNonEmpty(s.settingValue(ctx, "release_update_url"), s.UpdateURL)
	return &releasepublish.UpdateClient{BaseURL: base, Creds: s.credentials()}
}

// settingValue 读取界面保存的参数；没配置或读失败时返回空串，由调用方回落到环境变量。
func (s *Server) settingValue(ctx context.Context, key string) string {
	if s.Settings == nil {
		return ""
	}
	v, err := s.Settings.GetSetting(ctx, key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// runProfile loads the project release profile and returns it with the
// resolved file path used for hashing and diagnostics.
func (s *Server) runProfile(ctx context.Context, projectID string) (*releasev2.Profile, string, error) {
	project, err := s.Store.GetProject(ctx, projectID)
	if err != nil {
		return nil, "", err
	}
	rel := strings.TrimSpace(project.ReleaseProfile)
	if rel == "" {
		rel = "smartpipe.yaml"
	}
	path := rel
	if !filepath.IsAbs(path) {
		base := strings.TrimSpace(project.LocalRepoPath)
		if base == "" {
			return nil, "", fmt.Errorf("project has no local repository path")
		}
		path = filepath.Join(base, filepath.FromSlash(rel))
	}
	profile, err := releasev2.LoadProfile(path)
	if err != nil {
		return nil, path, err
	}
	return profile, path, nil
}

func (s *Server) dispatchWrite(ctx context.Context, p *principal, name string, args map[string]any) (*toolResult, bool, error) {
	switch name {
	case "build_dispatch":
		run, err := s.Service.DispatchBuild(ctx, p.Run.ID, "agent:"+p.Token.ID)
		if err != nil {
			return nil, true, err
		}
		return textResult(map[string]any{"run_id": run.ID, "status": run.Status}), true, nil

	case "build_wait":
		timeout := time.Duration(intArg(args, "timeout_ms", 20000)) * time.Millisecond
		if timeout > maxWaitBudget {
			timeout = maxWaitBudget
		}
		poll := time.Duration(intArg(args, "poll_ms", 1500)) * time.Millisecond
		if poll < 250*time.Millisecond {
			poll = 250 * time.Millisecond
		}
		deadline := s.now().Add(timeout)
		for {
			run, targets, err := s.Service.Progress(ctx, p.Run.ID)
			if err != nil {
				return nil, true, err
			}
			done := releasev2.TerminalRunStatus(run.Status) || allTargetsReady(targets)
			if done || !s.now().Before(deadline) {
				return textResult(map[string]any{"done": done, "timed_out": !done, "run": run, "targets": targets}), true, nil
			}
			select {
			case <-ctx.Done():
				return nil, true, ctx.Err()
			case <-time.After(poll):
			}
		}

	case "build_logs":
		chunks, err := s.Service.Logs(ctx, p.Run.ID, stringArg(args, "target"), int64Arg(args, "after"), intArg(args, "limit", 200))
		if err != nil {
			return nil, true, err
		}
		maxBytes := intArg(args, "max_bytes", 64<<10)
		var text strings.Builder
		cursor := int64Arg(args, "after")
		truncated := false
		for _, c := range chunks {
			if text.Len()+len(c.Chunk) > maxBytes {
				truncated = true
				break
			}
			text.WriteString(c.Chunk)
			cursor = c.EventID
		}
		return textResult(map[string]any{
			"chunks": len(chunks), "cursor": cursor, "truncated": truncated, "log": text.String(),
		}), true, nil

	case "build_retry":
		target := stringArg(args, "target")
		if target == "" {
			return nil, true, fmt.Errorf("target is required")
		}
		updated, err := s.Service.RetryTarget(ctx, p.Run.ID, target, "agent:"+p.Token.ID, stringArg(args, "reason"))
		if err != nil {
			return nil, true, err
		}
		return textResult(updated), true, nil

	case "release_notes_save":
		notes := stringArg(args, "notes")
		run, err := s.Service.SaveNotes(ctx, p.Run.ID, notes, "agent:"+p.Token.ID)
		if err != nil {
			return nil, true, err
		}
		return textResult(map[string]any{"run_id": run.ID, "notes_sha256": run.NotesSHA256, "length": len(run.NotesMD)}), true, nil

	case "artifact_verify":
		result, err := s.verifyArtifacts(ctx, p)
		if err != nil {
			return nil, true, err
		}
		return result, true, nil

	case "update_bundle_build":
		result, err := s.buildBundle(ctx, p)
		if err != nil {
			return nil, true, err
		}
		return result, true, nil

	case "update_import_stage":
		result, err := s.stageImport(ctx, p, args)
		if err != nil {
			return nil, true, err
		}
		return result, true, nil

	case "update_import_get":
		result, err := s.getImport(ctx, p, args)
		if err != nil {
			return nil, true, err
		}
		return result, true, nil

	case "update_channel_promote":
		result, err := s.promoteChannel(ctx, p, args)
		if err != nil {
			return nil, true, err
		}
		return result, true, nil

	case "release_publish":
		result, err := s.publish(ctx, p, args)
		if err != nil {
			return nil, true, err
		}
		return result, true, nil
	}
	return nil, false, nil
}

type bundleOutcome struct {
	BundleSHA256 string                   `json:"bundle_sha256"`
	StorageKey   string                   `json:"storage_key"`
	Size         int64                    `json:"size"`
	Slots        int                      `json:"slots"`
	Components   []string                 `json:"components"`
	Bundle       *releasev2.UpgradeBundle `json:"bundle"`
}

func (s *Server) loadBundleLayout(ctx context.Context, p *principal) (releasev2.BundleLayout, *releasev2.Profile, error) {
	profile, _, err := s.runProfile(ctx, p.Run.ProjectID)
	if err != nil {
		return releasev2.BundleLayout{}, nil, err
	}
	cfg := profile.Release.Publishing["update_service"]
	layout := releasev2.BundleLayout{
		Product:           firstNonEmpty(asString(cfg["component"]), "product"),
		Slots:             map[string]string{},
		Components:        map[string]string{},
		ComponentVersions: map[string]string{},
	}
	if url := asString(cfg["url"]); url != "" && s.Update == nil && s.UpdateURL == "" {
		s.UpdateURL = url
	}
	if slots, ok := cfg["productSlots"].(map[string]any); ok {
		for name, raw := range slots {
			entry, _ := raw.(map[string]any)
			role := asString(entry["artifactRole"])
			if role == "" {
				role = name
			}
			layout.Slots[name] = role
		}
	}
	if comps, ok := cfg["components"].(map[string]any); ok {
		for name, raw := range comps {
			entry, _ := raw.(map[string]any)
			if role := asString(entry["artifactRole"]); role != "" {
				layout.Components[role] = name
			}
			if version := asString(entry["version"]); version != "" {
				layout.ComponentVersions[name] = version
			}
		}
	}
	if sources, ok := cfg["sources"].(map[string]any); ok {
		layout.Sources.Primary = asString(sources["primary"])
		for _, m := range asStringSlice(sources["mirrors"]) {
			layout.Sources.Mirrors = append(layout.Sources.Mirrors, m)
		}
	}
	return layout, profile, nil
}

func (s *Server) buildBundle(ctx context.Context, p *principal) (*toolResult, error) {
	run, targets, artifacts, err := s.runSnapshot(ctx, p)
	if err != nil {
		return nil, err
	}
	if blocker := requireReadyTargets(targets); blocker != "" {
		return blockedResult("targets_not_ready", blocker), nil
	}
	if len(artifacts) == 0 {
		return blockedResult("artifacts_missing", "no verified artifacts are registered for this run"), nil
	}
	if s.Objects == nil {
		return blockedResult("storage_not_configured", "release artifact storage is not configured"), nil
	}
	layout, _, err := s.loadBundleLayout(ctx, p)
	if err != nil {
		return blockedResult("profile_error", err.Error()), nil
	}
	bundle, raw, digest, err := releasev2.BuildUpgradeBundle(run, targets, artifacts, layout, s.now())
	if err != nil {
		return blockedResult("bundle_invalid", err.Error()), nil
	}
	key := fmt.Sprintf("releases/%s/%s/upgrade-bundle.json", run.ID, run.Version)
	stored, err := s.Objects.Put(ctx, key, strings.NewReader(string(raw)), int64(len(raw)), "application/json")
	if err != nil {
		return nil, err
	}
	if stored.SHA256 != digest {
		return blockedResult("digest_mismatch", "storage digest does not match the canonical bundle digest"), nil
	}
	artifact, err := s.Store.AddArtifact(ctx, releasev2.Artifact{
		RunID: run.ID, TargetID: "", Attempt: 1, Role: "upgrade_bundle", Name: "upgrade-bundle.json",
		ContentType: "application/json", Size: stored.Size, SHA256: stored.SHA256, StorageKey: key,
	})
	if err != nil {
		return nil, err
	}
	if _, err := s.Store.UpsertPublication(ctx, releasev2.Publication{
		RunID: run.ID, Provider: "update_service", State: "bundle_ready", RequestSHA256: digest,
	}); err != nil {
		return nil, err
	}
	_ = s.Store.AppendEvent(ctx, releasev2.Event{
		RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "bundle.built",
		Summary: "upgrade bundle generated", Detail: map[string]any{"sha256": digest, "artifact_id": artifact.ID, "size": stored.Size},
	})
	names := make([]string, 0, len(bundle.Components))
	for name := range bundle.Components {
		names = append(names, name)
	}
	sort.Strings(names)
	return textResult(bundleOutcome{
		BundleSHA256: digest, StorageKey: key, Size: stored.Size,
		Slots: len(bundle.Components[layout.Product].Slots), Components: names, Bundle: bundle,
	}), nil
}

func (s *Server) runSnapshot(ctx context.Context, p *principal) (*releasev2.Run, []releasev2.Target, []releasev2.Artifact, error) {
	run, err := s.Service.GetRun(ctx, p.Run.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	targets, err := s.Store.ListTargets(ctx, run.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	artifacts, err := s.Store.ListArtifacts(ctx, run.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	return run, targets, artifacts, nil
}

func requireReadyTargets(targets []releasev2.Target) string {
	var pending []string
	for _, t := range targets {
		if t.Required && t.Status != releasev2.TargetReady {
			pending = append(pending, t.TargetID+"="+string(t.Status))
		}
	}
	if len(pending) == 0 {
		return ""
	}
	sort.Strings(pending)
	return "required targets are not ready: " + strings.Join(pending, ", ")
}

func allTargetsReady(targets []releasev2.TargetProgress) bool {
	ready := 0
	for _, t := range targets {
		if t.Status == releasev2.TargetReady {
			ready++
		}
	}
	return ready == len(targets) && ready > 0
}

func (s *Server) bundleObject(ctx context.Context, p *principal) (*releasev2.Artifact, []byte, error) {
	artifact, err := s.Store.GetArtifact(ctx, p.Run.ID, "", "upgrade_bundle", "upgrade-bundle.json")
	if err != nil {
		return nil, nil, err
	}
	if s.Objects == nil {
		return nil, nil, fmt.Errorf("release artifact storage is not configured")
	}
	reader, err := s.Objects.Open(ctx, artifact.StorageKey)
	if err != nil {
		return nil, nil, err
	}
	defer reader.Close()
	raw, err := io.ReadAll(io.LimitReader(reader, 8<<20))
	if err != nil {
		return nil, nil, err
	}
	if releasev2.HashText(string(raw)) != artifact.SHA256 {
		return nil, nil, fmt.Errorf("stored bundle digest does not match the registered artifact")
	}
	return artifact, raw, nil
}

// verifyArtifacts checks that every required target produced every declared
// artifact role and that the stored bytes still match the registered digest.
func (s *Server) verifyArtifacts(ctx context.Context, p *principal) (*toolResult, error) {
	_, targets, artifacts, err := s.runSnapshot(ctx, p)
	if err != nil {
		return nil, err
	}
	if s.Objects == nil {
		return blockedResult("storage_not_configured", "release artifact storage is not configured"), nil
	}
	blockers := artifactGaps(targets, artifacts)
	verified := 0
	for _, a := range artifacts {
		if strings.TrimSpace(a.StorageKey) == "" {
			blockers = append(blockers, fmt.Sprintf("%s/%s has no storage key", a.TargetID, a.Role))
			continue
		}
		stat, err := s.Objects.Stat(ctx, a.StorageKey)
		if err != nil {
			blockers = append(blockers, fmt.Sprintf("%s/%s storage: %v", a.TargetID, a.Role, err))
			continue
		}
		if stat.SHA256 == "" {
			// Objects written through a pre-signed URL carry no metadata digest;
			// fall back to hashing the stored bytes before declaring them valid.
			if digest, size, herr := hashStoredObject(ctx, s.Objects, a.StorageKey); herr != nil {
				blockers = append(blockers, fmt.Sprintf("%s/%s storage: %v", a.TargetID, a.Role, herr))
				continue
			} else {
				stat.SHA256, stat.Size = digest, size
			}
		}
		if stat.SHA256 != a.SHA256 || stat.Size != a.Size {
			blockers = append(blockers, fmt.Sprintf("%s/%s digest mismatch", a.TargetID, a.Role))
			continue
		}
		verified++
	}
	if len(blockers) > 0 {
		sort.Strings(blockers)
		return blockedResult("artifact_verification_failed", strings.Join(blockers, "; ")), nil
	}
	_ = s.Store.AppendEvent(ctx, releasev2.Event{
		RunID: p.Run.ID, Actor: "agent:" + p.Token.ID, Kind: "artifacts.verified",
		Summary: "artifacts verified", Detail: map[string]any{"artifacts": verified, "targets": len(targets)},
	})
	return textResult(map[string]any{"verified": verified, "targets": len(targets), "state": "ok"}), nil
}

func hashStoredObject(ctx context.Context, objects releasev2.ObjectStore, key string) (string, int64, error) {
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

func artifactGaps(targets []releasev2.Target, artifacts []releasev2.Artifact) []string {
	have := map[string]bool{}
	for _, a := range artifacts {
		have[a.TargetID+"|"+a.Role] = true
	}
	var gaps []string
	for _, t := range targets {
		if !t.Required {
			continue
		}
		for _, rule := range t.Artifacts {
			if !have[t.TargetID+"|"+rule.Role] {
				gaps = append(gaps, fmt.Sprintf("%s missing artifact role %s", t.TargetID, rule.Role))
			}
		}
	}
	return gaps
}

// finishPublish publishes the GitHub draft when one exists, verifies its asset
// digests, and records partially_published when GitHub fails after the channel
// pointer already moved.
func (s *Server) finishPublish(ctx context.Context, p *principal, run *releasev2.Run, targets []releasev2.Target, artifacts []releasev2.Artifact, ghCredential string) {
	runID := run.ID
	from := run.Status
	actor := "agent:" + p.Token.ID
	githubID := s.githubDraftID(ctx, runID)
	if githubID != 0 {
		owner, repo, err := s.githubRepo(ctx, p.Run.ProjectID)
		if err != nil {
			_, _ = s.Service.Transition(ctx, runID, from, releasev2.RunPartiallyPublished, actor, "github repository unavailable")
			return
		}
		release, err := s.githubClient(ctx).GetRelease(ctx, ghCredential, owner, repo, githubID)
		if err == nil {
			if gaps := verifyGitHubAssets(release, artifacts); len(gaps) > 0 {
				_, _ = s.Service.Transition(ctx, runID, from, releasev2.RunPartiallyPublished, actor, strings.Join(gaps, "; "))
				_ = s.Store.AppendEvent(ctx, releasev2.Event{RunID: runID, Actor: actor, Kind: "github.verify_failed",
					Summary: "github assets incomplete", Detail: map[string]any{"gaps": gaps}})
				return
			}
		}
		published, err := s.githubClient(ctx).PublishRelease(ctx, ghCredential, owner, repo, githubID)
		if err != nil {
			_, _ = s.Service.Transition(ctx, runID, from, releasev2.RunPartiallyPublished, actor, "github publish failed: "+err.Error())
			_ = s.Store.AppendEvent(ctx, releasev2.Event{RunID: runID, Actor: actor, Kind: "github.publish_failed",
				Summary: "github publish failed after channel promote", Detail: map[string]any{"error": err.Error()}})
			return
		}
		_, _ = s.Store.UpsertPublication(ctx, releasev2.Publication{RunID: runID, Provider: "github", State: "published",
			ExternalID: fmt.Sprint(published.ID), ExternalURL: published.HTMLURL})
		_ = s.Store.AppendEvent(ctx, releasev2.Event{RunID: runID, Actor: actor, Kind: "github.published",
			Summary: "github release published", Detail: map[string]any{"url": published.HTMLURL}})
	}
	_, _ = s.Service.Transition(ctx, runID, from, releasev2.RunPublished, actor, "channel and release verified")
}

// updateCredential 解析升级服务凭据引用：显式参数 > 项目发版配置 > 服务端默认。
// 返回值永远是引用名，凭据明文只在适配器内部解析，不进事件与日志。
func (s *Server) updateCredential(ctx context.Context, projectID string, args map[string]any) string {
	profile, _, err := s.runProfile(ctx, projectID)
	if err != nil {
		profile = nil
	}
	fallback := firstNonEmpty(s.settingValue(ctx, "release_update_credential"), s.UpdateCredential)
	return updateCredentialFrom(profile, stringArg(args, "credential"), fallback)
}

// updateCredentialFrom 是本包的凭据引用解析规则，单独抽出来便于测试。
func updateCredentialFrom(profile *releasev2.Profile, argRef, fallback string) string {
	if ref := strings.TrimSpace(argRef); ref != "" {
		return ref
	}
	if profile != nil && profile.Release != nil {
		if cfg := profile.Release.Publishing["update_service"]; cfg != nil {
			if ref := strings.TrimSpace(asString(cfg["credential"])); ref != "" {
				return ref
			}
		}
	}
	return strings.TrimSpace(fallback)
}
