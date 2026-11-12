package releasemcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

func (s *Server) updateClient() *releasepublish.UpdateClient {
	if s.Update != nil {
		return s.Update
	}
	return &releasepublish.UpdateClient{BaseURL: s.UpdateURL, Creds: s.credentials()}
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

func (s *Server) stageImport(ctx context.Context, p *principal, args map[string]any) (*toolResult, error) {
	run, targets, artifacts, err := s.runSnapshot(ctx, p)
	if err != nil {
		return nil, err
	}
	if run.Mode == releasev2.ModeBuildOnly {
		return blockedResult("mode_not_publishable", "build_only runs cannot be staged to the upgrade service"), nil
	}
	if blocker := requireReadyTargets(targets); blocker != "" {
		return blockedResult("targets_not_ready", blocker), nil
	}
	_ = artifacts
	artifact, raw, err := s.bundleObject(ctx, p)
	if err != nil {
		return blockedResult("bundle_missing", "build the upgrade bundle first: "+err.Error()), nil
	}
	layout, profile, err := s.loadBundleLayout(ctx, p)
	if err != nil {
		return blockedResult("profile_error", err.Error()), nil
	}
	client := s.updateClient()
	if strings.TrimSpace(client.BaseURL) == "" {
		return blockedResult("update_service_not_configured", "upgrade service URL is not configured"), nil
	}
	credential := s.updateCredential(ctx, run.ProjectID, args)
	if credential == "" {
		return blockedResult("credential_required", "no upgrade service credential configured: pass credential or set publishing.update_service.credential"), nil
	}
	req := releasepublish.ImportRequest{
		Product: layout.Product, Channel: firstNonEmpty(stringArg(args, "channel"), run.Channel),
		ProductVersion: run.Version, BuildVersion: run.BuildVersion, BundleSHA256: artifact.SHA256,
		IdempotencyKey: fmt.Sprintf("stage-%s-%s", run.ID, strings.TrimPrefix(artifact.SHA256, "sha256:")[:12]),
	}
	if s.Presigner != nil {
		if url, err := s.Presigner.PresignGet(ctx, artifact.StorageKey, 30*time.Minute); err == nil {
			req.BundleURL = url
		}
	}
	if req.BundleURL == "" {
		req.BundleJSON = json.RawMessage(raw)
	}
	imp, err := client.Stage(ctx, credential, req)
	if err != nil {
		return nil, err
	}
	if _, err := s.Store.UpsertPublication(ctx, releasev2.Publication{
		RunID: run.ID, Provider: "update_service", State: imp.State, ExternalID: imp.ID,
		ExternalURL: req.BundleURL, RequestSHA256: artifact.SHA256,
	}); err != nil {
		return nil, err
	}
	_ = s.Store.AppendEvent(ctx, releasev2.Event{
		RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "update_import.staged",
		Summary: "upgrade service staging import created",
		Detail:  map[string]any{"import_id": imp.ID, "state": imp.State, "bom_sha256": imp.BOMSHA256, "channel": imp.Channel},
	})
	_ = profile
	if run.Status == releasev2.RunVerifying || run.Status == releasev2.RunBuilding {
		if _, err := s.Service.Transition(ctx, run.ID, run.Status, releasev2.RunStaged, "agent:"+p.Token.ID, "upgrade bundle staged"); err == nil {
			run, _ = s.Service.GetRun(ctx, run.ID)
		}
	}
	return textResult(map[string]any{"import": imp, "run_status": run.Status}), nil
}

func (s *Server) getImport(ctx context.Context, p *principal, args map[string]any) (*toolResult, error) {
	importID := stringArg(args, "import_id")
	if importID == "" {
		pubs, err := s.Store.ListPublications(ctx, p.Run.ID)
		if err != nil {
			return nil, err
		}
		for _, pub := range pubs {
			if pub.Provider == "update_service" && pub.ExternalID != "" {
				importID = pub.ExternalID
			}
		}
	}
	if importID == "" {
		return blockedResult("import_missing", "no staging import exists for this run"), nil
	}
	credential := s.updateCredential(ctx, p.Run.ProjectID, args)
	if credential == "" {
		return blockedResult("credential_required", "no upgrade service credential configured for this project"), nil
	}
	imp, err := s.updateClient().Get(ctx, credential, importID)
	if err != nil {
		return nil, err
	}
	return textResult(imp), nil
}

func (s *Server) promoteChannel(ctx context.Context, p *principal, args map[string]any) (*toolResult, error) {
	return s.promote(ctx, p, args, false)
}

func (s *Server) publish(ctx context.Context, p *principal, args map[string]any) (*toolResult, error) {
	run, err := s.Service.GetRun(ctx, p.Run.ID)
	if err != nil {
		return nil, err
	}
	if run.Mode == releasev2.ModeBuildOnly {
		return blockedResult("mode_not_publishable", "build_only runs do not publish"), nil
	}
	return s.promote(ctx, p, args, true)
}

func (s *Server) promote(ctx context.Context, p *principal, args map[string]any, final bool) (*toolResult, error) {
	run, targets, artifacts, err := s.runSnapshot(ctx, p)
	if err != nil {
		return nil, err
	}
	if blocker := requireReadyTargets(targets); blocker != "" {
		return blockedResult("targets_not_ready", blocker), nil
	}
	if profile, _, perr := s.runProfile(ctx, run.ProjectID); perr == nil && profile.Release != nil {
		if releasev2.SignatureRequired(profile.Release.Policies) {
			var unsigned []string
			for _, artifact := range artifacts {
				if artifact.Role == "upgrade_bundle" || artifact.PlatformSigned {
					continue
				}
				unsigned = append(unsigned, artifact.TargetID+"/"+artifact.Name)
			}
			if len(unsigned) > 0 {
				sort.Strings(unsigned)
				return blockedResult("unsigned_artifacts", "release profile requires platform signatures; unsigned: "+strings.Join(unsigned, ", ")), nil
			}
		}
	}
	pubs, err := s.Store.ListPublications(ctx, p.Run.ID)
	if err != nil {
		return nil, err
	}
	var pub *releasev2.Publication
	for i := range pubs {
		if pubs[i].Provider == "update_service" && pubs[i].ExternalID != "" {
			pub = &pubs[i]
		}
	}
	if pub == nil {
		return blockedResult("import_missing", "stage the upgrade bundle before promoting the channel"), nil
	}
	if !strings.EqualFold(pub.State, "promoted") && !strings.EqualFold(pub.State, "published") {
		if profile, _, perr := s.runProfile(ctx, run.ProjectID); perr == nil {
			if required, _ := releasev2.ApprovalPolicy(profile.Release.Policies, run.Channel); required {
				approval, aerr := s.Store.LatestValidApproval(ctx, run.ID, "publish", pub.RequestSHA256)
				if aerr != nil {
					return blockedResult("approval_required", fmt.Sprintf(
						"channel %s requires a human approval bound to bundle %s", run.Channel, pub.RequestSHA256)), nil
				}
				_ = s.Store.AppendEvent(ctx, releasev2.Event{RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "approval.checked",
					Summary: "publish approval verified", Detail: map[string]any{"approval_id": approval.ID, "actor": approval.Actor}})
			}
		}
	}
	credential := s.updateCredential(ctx, run.ProjectID, args)
	if credential == "" {
		return blockedResult("credential_required", "no upgrade service credential configured: pass credential or set publishing.update_service.credential"), nil
	}
	expectedVersion := stringArg(args, "expected_current_version")
	expectedBOM := stringArg(args, "expected_bom_sha256")
	client := s.updateClient()
	if expectedVersion == "" && expectedBOM == "" {
		pointer, lerr := client.Latest(ctx, credential, run.Channel)
		switch {
		case lerr == nil:
			expectedVersion, expectedBOM = pointer.ProductVersion, pointer.BOMSHA256
		case errors.Is(lerr, releasepublish.ErrNotFoundRemote):
			// First release on this channel: no current pointer to compare.
		default:
			return blockedResult("channel_preflight_failed", "cannot read current channel pointer: "+lerr.Error()), nil
		}
	}
	var result *releasepublish.PromoteResult
	if strings.EqualFold(pub.State, "promoted") || strings.EqualFold(pub.State, "published") {
		// Resuming after a partial publication: the channel already points at
		// this import, so only the remaining steps need to run.
		result = &releasepublish.PromoteResult{State: "promoted", Channel: run.Channel,
			ProductVersion: run.Version, ReleaseID: pub.ExternalID, BOMSHA256: pub.RequestSHA256}
	} else {
		result, err = client.Promote(ctx, credential, pub.ExternalID, releasepublish.PromoteRequest{
			ExpectedCurrentProductVersion: expectedVersion,
			ExpectedBOMSHA256:             expectedBOM,
			IdempotencyKey:                fmt.Sprintf("promote-%s-%d", run.ID, pub.ID),
		})
	}
	if err != nil {
		_ = s.Store.AppendEvent(ctx, releasev2.Event{RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "channel.promote_failed",
			Summary: "channel promote failed", Detail: map[string]any{"error": err.Error()}})
		return nil, err
	}
	if _, err := s.Store.UpsertPublication(ctx, releasev2.Publication{
		RunID: run.ID, Provider: "update_service", State: result.State, ExternalID: pub.ExternalID,
		ExternalURL: pub.ExternalURL, RequestSHA256: pub.RequestSHA256,
	}); err != nil {
		return nil, err
	}
	if run.Status == releasev2.RunVerifying || run.Status == releasev2.RunBuilding {
		if ok, err := s.Service.Transition(ctx, run.ID, run.Status, releasev2.RunStaged, "agent:"+p.Token.ID, "channel promote requested"); err == nil && ok {
			run.Status = releasev2.RunStaged
		}
	}
	if run.Status == releasev2.RunStaged {
		if ok, err := s.Service.Transition(ctx, run.ID, releasev2.RunStaged, releasev2.RunPublishing, "agent:"+p.Token.ID, "channel promote started"); err == nil && ok {
			run.Status = releasev2.RunPublishing
		}
	}
	if run.Status == releasev2.RunPublishing {
		switch {
		case final:
			s.finishPublish(ctx, p, run, targets, artifacts, s.githubCredential(args))
		case s.githubDraftID(ctx, run.ID) == 0:
			// Channel promote is the last external step for stage runs and for
			// projects without a GitHub release target.
			_, _ = s.Service.Transition(ctx, run.ID, releasev2.RunPublishing, releasev2.RunPublished, "agent:"+p.Token.ID, "channel promoted and verified")
		}
	}
	// 渠道已经推进、只差 GitHub 收口的运行停在 partially_published；此时再调
	// release_publish 只补最后一步，不重复 promote 渠道。
	if final && run.Status == releasev2.RunPartiallyPublished {
		_ = s.Store.AppendEvent(ctx, releasev2.Event{RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "release.resume",
			Summary: "resuming partial publication", Detail: map[string]any{"from": run.Status}})
		s.finishPublish(ctx, p, run, targets, artifacts, s.githubCredential(args))
	}
	if pointer, lerr := client.Latest(ctx, stringArg(args, "credential"), result.Channel); lerr == nil {
		if pointer.BOMSHA256 != "" && result.BOMSHA256 != "" && pointer.BOMSHA256 != result.BOMSHA256 {
			_ = s.Store.AppendEvent(ctx, releasev2.Event{RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "channel.verify_failed",
				Summary: "channel pointer does not match promoted BOM",
				Detail:  map[string]any{"expected": result.BOMSHA256, "actual": pointer.BOMSHA256}})
			_, _ = s.Service.Transition(ctx, run.ID, run.Status, releasev2.RunPartiallyPublished, "agent:"+p.Token.ID, "channel verification failed")
			return blockedResult("channel_verify_failed", fmt.Sprintf("channel %s reports BOM %s, expected %s", result.Channel, pointer.BOMSHA256, result.BOMSHA256)), nil
		}
		_ = s.Store.AppendEvent(ctx, releasev2.Event{RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "channel.verified",
			Summary: "channel pointer verified", Detail: map[string]any{"channel": pointer.Channel, "bom_sha256": pointer.BOMSHA256}})
	}
	_ = s.Store.AppendEvent(ctx, releasev2.Event{
		RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "channel.promoted",
		Summary: "channel pointer advanced",
		Detail:  map[string]any{"channel": result.Channel, "bom_sha256": result.BOMSHA256, "release_id": result.ReleaseID},
	})
	latest, err := s.Service.GetRun(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	return textResult(map[string]any{"promote": result, "run": latest}), nil
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
		release, err := s.githubClient().GetRelease(ctx, ghCredential, owner, repo, githubID)
		if err == nil {
			if gaps := verifyGitHubAssets(release, artifacts); len(gaps) > 0 {
				_, _ = s.Service.Transition(ctx, runID, from, releasev2.RunPartiallyPublished, actor, strings.Join(gaps, "; "))
				_ = s.Store.AppendEvent(ctx, releasev2.Event{RunID: runID, Actor: actor, Kind: "github.verify_failed",
					Summary: "github assets incomplete", Detail: map[string]any{"gaps": gaps}})
				return
			}
		}
		published, err := s.githubClient().PublishRelease(ctx, ghCredential, owner, repo, githubID)
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
	return updateCredentialFrom(profile, stringArg(args, "credential"), s.UpdateCredential)
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
