package releasemcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"smartpipe/backend/internal/releasepublish"
	"smartpipe/backend/internal/releasev2"
)

// stageImport creates one unpublished release per bundle component and uploads
// every artifact the bundle references. Nothing is visible to clients until the
// channel pointers move during promote, so a failed upload leaves the current
// channel untouched.
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
	if s.Objects == nil {
		return blockedResult("storage_not_configured", "release artifact storage is not configured"), nil
	}
	artifact, raw, err := s.bundleObject(ctx, p)
	if err != nil {
		return blockedResult("bundle_missing", "build the upgrade bundle first: "+err.Error()), nil
	}
	var bundle releasev2.UpgradeBundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return blockedResult("bundle_invalid", "upgrade bundle is not readable: "+err.Error()), nil
	}
	if len(bundle.Components) == 0 {
		return blockedResult("bundle_invalid", "upgrade bundle has no components"), nil
	}
	layout, _, err := s.loadBundleLayout(ctx, p)
	if err != nil {
		return blockedResult("profile_error", err.Error()), nil
	}
	client := s.updateClient(ctx)
	if strings.TrimSpace(client.BaseURL) == "" {
		return blockedResult("update_service_not_configured", "upgrade service URL is not configured"), nil
	}
	credential := s.updateCredential(ctx, run.ProjectID, args)
	if credential == "" {
		return blockedResult("credential_required", "no upgrade service credential configured: pass credential or set publishing.update_service.credential"), nil
	}
	channel := firstNonEmpty(stringArg(args, "channel"), run.Channel)
	productName := firstNonEmpty(layout.Product, "product")

	byID := make(map[int64]releasev2.Artifact, len(artifacts))
	for _, a := range artifacts {
		byID[a.ID] = a
	}
	names := make([]string, 0, len(bundle.Components))
	for name := range bundle.Components {
		names = append(names, name)
	}
	sort.Strings(names)

	stages := make([]map[string]any, 0, len(names))
	releaseIDs := map[string]string{}
	uploaded, skipped := 0, 0
	for _, name := range names {
		component := bundle.Components[name]
		if !isTripleVersion(component.Version) {
			return blockedResult("version_invalid", fmt.Sprintf(
				"component %s version %q must be MAJOR.MINOR.PATCH; the upgrade service rejects other shapes", name, component.Version)), nil
		}
		kind, versioning := "content", "independent"
		if name == productName {
			kind, versioning = "product", "product"
		}
		if _, err := client.EnsureComponent(ctx, credential, releasepublish.EnsureComponentRequest{
			Name: name, DisplayName: name, Kind: kind, Versioning: versioning,
		}); err != nil {
			return blockedResult("update_component_failed", fmt.Sprintf("ensure component %s: %v", name, err)), nil
		}
		rel, err := client.FindRelease(ctx, credential, name, channel, component.Version)
		switch {
		case errors.Is(err, releasepublish.ErrNotFoundRemote):
			rel, err = client.CreateRelease(ctx, credential, releasepublish.CreateReleaseRequest{
				Component: name, Version: component.Version, Channel: channel,
			})
			if err != nil {
				return blockedResult("update_release_failed", fmt.Sprintf("create release %s@%s: %v", name, component.Version, err)), nil
			}
		case err != nil:
			return blockedResult("update_release_failed", fmt.Sprintf("find release %s@%s: %v", name, component.Version, err)), nil
		}
		releaseIDs[name] = rel.ID

		// Re-uploading an identical artifact is a no-op: the service stores
		// bytes by digest, so a retry after a partial stage only sends the
		// missing cells.
		existing := map[string]string{}
		if detail, derr := client.GetRelease(ctx, credential, rel.ID); derr == nil {
			for _, uploadedArtifact := range detail.Artifacts {
				existing[artifactCell(uploadedArtifact.Slot, uploadedArtifact.OS, uploadedArtifact.Arch)] = normalizeDigest(uploadedArtifact.SHA256)
			}
		}
		componentUploaded, componentSkipped := 0, 0
		upload := func(slot, platformOS, arch, filename, digest string, artifactID, size int64) error {
			source, ok := byID[artifactID]
			if !ok {
				return fmt.Errorf("bundle references artifact %d which is not registered for this run", artifactID)
			}
			if strings.TrimSpace(source.StorageKey) == "" {
				return fmt.Errorf("artifact %s has no storage key", source.Name)
			}
			digest = normalizeDigest(digest)
			cell := artifactCell(slot, platformOS, arch)
			if digest != "" && existing[cell] == digest {
				componentSkipped++
				return nil
			}
			reader, err := s.Objects.Open(ctx, source.StorageKey)
			if err != nil {
				return fmt.Errorf("open artifact %s: %w", source.Name, err)
			}
			if size <= 0 {
				size = source.Size
			}
			_, uploadErr := client.UploadArtifact(ctx, credential, rel.ID, slot, platformOS, arch, filename, digest, size, reader)
			closeErr := reader.Close()
			if uploadErr != nil {
				return fmt.Errorf("upload %s %s: %w", name, cell, uploadErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close artifact %s: %w", source.Name, closeErr)
			}
			existing[cell] = digest
			componentUploaded++
			return nil
		}
		for _, slot := range component.Slots {
			if err := upload(slot.Slot, slot.OS, slot.Arch, slot.Name, slot.SHA256, slot.ArtifactID, slot.Size); err != nil {
				return blockedResult("update_upload_failed", err.Error()), nil
			}
		}
		for _, extra := range component.Artifacts {
			filename, ferr := contentArtifactName(name, component.Version, extra.Name)
			if ferr != nil {
				return blockedResult("artifact_name_invalid", ferr.Error()), nil
			}
			if err := upload(name, "", "", filename, extra.SHA256, extra.ArtifactID, extra.Size); err != nil {
				return blockedResult("update_upload_failed", err.Error()), nil
			}
		}
		uploaded += componentUploaded
		skipped += componentSkipped
		stages = append(stages, map[string]any{
			"component": name, "version": component.Version, "release_id": rel.ID,
			"kind": kind, "uploaded": componentUploaded, "skipped": componentSkipped,
		})
	}

	productID := releaseIDs[productName]
	if productID == "" {
		return blockedResult("bundle_invalid", "upgrade bundle has no product component"), nil
	}
	if _, err := s.Store.UpsertPublication(ctx, releasev2.Publication{
		RunID: run.ID, Provider: "update_service", State: "staged", ExternalID: productID,
		ExternalURL:   strings.TrimRight(client.BaseURL, "/") + "/v1/admin/releases/" + productID,
		RequestSHA256: artifact.SHA256,
	}); err != nil {
		return nil, err
	}
	_ = s.Store.AppendEvent(ctx, releasev2.Event{
		RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "update_import.staged",
		Summary: "upgrade service releases staged and artifacts uploaded",
		Detail: map[string]any{
			"channel": channel, "product_release_id": productID, "releases": stages,
			"uploaded": uploaded, "skipped": skipped, "bundle_sha256": artifact.SHA256,
		},
	})
	if run.Status == releasev2.RunVerifying || run.Status == releasev2.RunBuilding {
		if _, err := s.Service.Transition(ctx, run.ID, run.Status, releasev2.RunStaged, "agent:"+p.Token.ID, "upgrade bundle staged"); err == nil {
			run, _ = s.Service.GetRun(ctx, run.ID)
		}
	}
	return textResult(map[string]any{
		"stage": map[string]any{
			"id": productID, "state": "staged", "channel": channel,
			"product_version": run.Version, "releases": stages,
			"uploaded": uploaded, "skipped": skipped,
			"update_service": strings.TrimRight(client.BaseURL, "/"),
		},
		"run_status": run.Status,
	}), nil
}

// getImport reads one staged release with its artifact matrix, plus the current
// channel pointer so callers can see what clients are being served right now.
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
	client := s.updateClient(ctx)
	detail, err := client.GetRelease(ctx, credential, importID)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"release": detail.Release, "artifacts": detail.Artifacts}
	if pointer, perr := client.Latest(ctx, credential, p.Run.Channel); perr == nil {
		out["channel"] = pointer
	}
	return textResult(out), nil
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

// promote moves every staged component pointer to the run channel, publishes
// the channel BOM and reads the signed distribution back before reporting
// success. Repeating it is safe: the service treats identical publishes as
// updates of the same BOM.
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
	client := s.updateClient(ctx)
	channel := run.Channel
	expectedVersion := stringArg(args, "expected_current_version")
	expectedBOM := stringArg(args, "expected_bom_sha256")
	if expectedVersion != "" || expectedBOM != "" {
		pointer, lerr := client.Latest(ctx, credential, channel)
		switch {
		case lerr == nil:
			if expectedVersion != "" && pointer.ProductVersion != expectedVersion {
				return blockedResult("channel_preflight_failed", fmt.Sprintf(
					"channel %s serves product version %s, expected %s", channel, pointer.ProductVersion, expectedVersion)), nil
			}
			if expectedBOM != "" && pointer.BOMSHA256 != expectedBOM {
				return blockedResult("channel_preflight_failed", fmt.Sprintf(
					"channel %s serves BOM %s, expected %s", channel, pointer.BOMSHA256, expectedBOM)), nil
			}
		case errors.Is(lerr, releasepublish.ErrNotFoundRemote):
			// First release on this channel: nothing to compare against.
		default:
			return blockedResult("channel_preflight_failed", "cannot read current channel pointer: "+lerr.Error()), nil
		}
	}
	outcome, err := s.publishStaged(ctx, p, client, credential, channel, run)
	if err != nil {
		_ = s.Store.AppendEvent(ctx, releasev2.Event{RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "channel.promote_failed",
			Summary: "channel promote failed", Detail: map[string]any{"error": err.Error()}})
		return blockedResult("channel_promote_failed", err.Error()), nil
	}
	if _, err := s.Store.UpsertPublication(ctx, releasev2.Publication{
		RunID: run.ID, Provider: "update_service", State: "published", ExternalID: pub.ExternalID,
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
			s.finishPublish(ctx, p, run, targets, artifacts, s.githubCredential(ctx, args))
		case s.githubDraftID(ctx, run.ID) == 0:
			// Channel promote is the last external step for stage runs and for
			// projects without a GitHub release target.
			_, _ = s.Service.Transition(ctx, run.ID, releasev2.RunPublishing, releasev2.RunPublished, "agent:"+p.Token.ID, "channel promoted and verified")
		}
	}
	// The channel is already advanced and only GitHub is missing: a second
	// release_publish call finishes that step without touching the channel.
	if final && run.Status == releasev2.RunPartiallyPublished {
		_ = s.Store.AppendEvent(ctx, releasev2.Event{RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "release.resume",
			Summary: "resuming partial publication", Detail: map[string]any{"from": run.Status}})
		s.finishPublish(ctx, p, run, targets, artifacts, s.githubCredential(ctx, args))
	}
	_ = s.Store.AppendEvent(ctx, releasev2.Event{
		RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "channel.promoted",
		Summary: "channel pointer advanced and distribution verified",
		Detail:  outcome,
	})
	latest, err := s.Service.GetRun(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	return textResult(map[string]any{"promote": outcome, "run": latest}), nil
}

// publishStaged is the domain step behind promote: point every component
// channel pointer at its staged release, publish the BOM, then read the signed
// distribution back and compare it with what was requested.
func (s *Server) publishStaged(ctx context.Context, p *principal, client *releasepublish.UpdateClient, credential, channel string, run *releasev2.Run) (map[string]any, error) {
	_, raw, err := s.bundleObject(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("read upgrade bundle: %w", err)
	}
	var bundle releasev2.UpgradeBundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return nil, fmt.Errorf("decode upgrade bundle: %w", err)
	}
	layout, _, err := s.loadBundleLayout(ctx, p)
	if err != nil {
		return nil, err
	}
	productName := firstNonEmpty(layout.Product, "product")
	names := make([]string, 0, len(bundle.Components))
	for name := range bundle.Components {
		names = append(names, name)
	}
	sort.Strings(names)
	bom := make([]releasepublish.DistributionComponent, 0, len(names))
	releases := make([]map[string]any, 0, len(names))
	productReleaseID := ""
	for _, name := range names {
		component := bundle.Components[name]
		rel, err := client.FindRelease(ctx, credential, name, channel, component.Version)
		if errors.Is(err, releasepublish.ErrNotFoundRemote) {
			return nil, fmt.Errorf("component %s@%s is not staged on channel %s; run update_import_stage again", name, component.Version, channel)
		}
		if err != nil {
			return nil, fmt.Errorf("find release %s@%s: %w", name, component.Version, err)
		}
		if err := client.PublishRelease(ctx, credential, rel.ID); err != nil {
			return nil, fmt.Errorf("publish %s@%s: %w", name, component.Version, err)
		}
		bom = append(bom, releasepublish.DistributionComponent{Name: name, Version: component.Version, ReleaseID: rel.ID})
		releases = append(releases, map[string]any{"component": name, "version": component.Version, "release_id": rel.ID})
		if name == productName {
			productReleaseID = rel.ID
		}
	}
	if productReleaseID == "" {
		return nil, fmt.Errorf("upgrade bundle has no %q component", productName)
	}
	existing, derr := client.GetDistribution(ctx, credential, channel, run.Version)
	switch {
	case derr == nil && sameDistribution(existing.Components, bom):
		// The BOM already pins exactly these versions.
	case derr != nil && !errors.Is(derr, releasepublish.ErrNotFoundRemote):
		return nil, fmt.Errorf("read distribution %s/%s: %w", channel, run.Version, derr)
	default:
		if _, err := client.PublishDistribution(ctx, credential, releasepublish.PublishDistributionRequest{
			Channel: channel, ProductVersion: run.Version, Components: bom,
		}); err != nil {
			return nil, fmt.Errorf("publish distribution %s/%s: %w", channel, run.Version, err)
		}
	}
	pointer, err := client.Latest(ctx, credential, channel)
	if err != nil {
		return nil, fmt.Errorf("read back channel %s: %w", channel, err)
	}
	if pointer.ProductVersion != run.Version {
		return nil, fmt.Errorf("channel %s serves product version %s, expected %s", channel, pointer.ProductVersion, run.Version)
	}
	if err := compareDistribution(pointer.Components, bom); err != nil {
		return nil, err
	}
	return map[string]any{
		"state": "published", "channel": channel, "product_version": run.Version,
		"release_id": productReleaseID, "bom_sha256": pointer.BOMSHA256,
		"published_at": pointer.PublishedAt, "releases": releases,
	}, nil
}

func sameDistribution(current, next []releasepublish.DistributionComponent) bool {
	if len(current) != len(next) {
		return false
	}
	byName := make(map[string]string, len(current))
	for _, component := range current {
		byName[component.Name] = component.Version
	}
	for _, component := range next {
		if byName[component.Name] != component.Version {
			return false
		}
	}
	return true
}

func compareDistribution(current, expected []releasepublish.DistributionComponent) error {
	byName := make(map[string]string, len(current))
	for _, component := range current {
		byName[component.Name] = component.Version
	}
	var mismatches []string
	for _, component := range expected {
		if byName[component.Name] != component.Version {
			mismatches = append(mismatches, fmt.Sprintf("%s=%s (serving %q)", component.Name, component.Version, byName[component.Name]))
		}
	}
	if len(mismatches) > 0 {
		return fmt.Errorf("channel distribution does not match the staged components: %s", strings.Join(mismatches, ", "))
	}
	return nil
}

// artifactCell is the identity of an artifact in a release.
func artifactCell(slot, platformOS, arch string) string {
	if platformOS == "" && arch == "" {
		return slot + "/content"
	}
	return slot + "/" + platformOS + "/" + arch
}

func normalizeDigest(value string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "sha256:")
}

// contentArtifactName enforces the service naming rule for content components:
// <component>-<version>.tar.gz. A local archive that already carries another
// name is uploaded under the canonical one, because clients look it up by name.
func contentArtifactName(component, version, filename string) (string, error) {
	expected := fmt.Sprintf("%s-%s.tar.gz", component, version)
	name := strings.TrimSpace(filename)
	if name == expected {
		return name, nil
	}
	if strings.HasSuffix(strings.ToLower(name), ".tar.gz") {
		return expected, nil
	}
	return "", fmt.Errorf("content artifact %s must be a .tar.gz archive named %s", name, expected)
}

func isTripleVersion(value string) bool {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}
