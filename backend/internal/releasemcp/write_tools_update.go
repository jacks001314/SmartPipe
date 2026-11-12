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

// updateTarget 是一台配置好的升级服务。发布时同一份制品要灌到每一台，
// 所以地址、凭据和发布记录都按服务分开。
type updateTarget struct {
	Name       string
	URL        string
	Credential string
	Client     *releasepublish.UpdateClient
}

// updateTargets 读取界面维护的升级服务列表；没有列表时回落到旧的单服务字段。
func (s *Server) updateTargets(ctx context.Context) []updateTarget {
	if s.Update != nil {
		return []updateTarget{{Name: "default", URL: s.Update.BaseURL, Client: s.Update}}
	}
	if raw := s.settingValue(ctx, "release_update_services"); raw != "" {
		var entries []struct {
			Name       string `json:"name"`
			URL        string `json:"url"`
			Credential string `json:"credential"`
			Enabled    bool   `json:"enabled"`
		}
		if err := json.Unmarshal([]byte(raw), &entries); err == nil {
			targets := make([]updateTarget, 0, len(entries))
			for i, entry := range entries {
				url := strings.TrimRight(strings.TrimSpace(entry.URL), "/")
				if !entry.Enabled || url == "" {
					continue
				}
				name := strings.TrimSpace(entry.Name)
				if name == "" {
					name = fmt.Sprintf("update-%d", i+1)
				}
				targets = append(targets, updateTarget{
					Name: name, URL: url, Credential: strings.TrimSpace(entry.Credential),
					Client: &releasepublish.UpdateClient{BaseURL: url, Creds: s.credentials()},
				})
			}
			if len(targets) > 0 {
				return targets
			}
		}
	}
	base := firstNonEmpty(s.settingValue(ctx, "release_update_url"), s.UpdateURL)
	if strings.TrimSpace(base) == "" {
		return nil
	}
	return []updateTarget{{Name: "default", URL: strings.TrimRight(base, "/"), Client: &releasepublish.UpdateClient{BaseURL: base, Creds: s.credentials()}}}
}

// stageImport creates one unpublished release per bundle component on every
// configured upgrade service and uploads every referenced artifact. Nothing is
// visible to clients until the channel pointers move during promote, so a
// failed upload leaves the current channels untouched.
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
	services := s.updateTargets(ctx)
	if len(services) == 0 {
		return blockedResult("update_service_not_configured", "发版设置里还没有配置升级服务地址"), nil
	}
	fallbackCredential := s.updateCredential(ctx, run.ProjectID, args)
	channel := firstNonEmpty(stringArg(args, "channel"), run.Channel)

	byID := make(map[int64]releasev2.Artifact, len(artifacts))
	for _, a := range artifacts {
		byID[a.ID] = a
	}
	results := make([]map[string]any, 0, len(services))
	uploaded, skipped := 0, 0
	primaryID := ""
	for _, service := range services {
		credential := firstNonEmpty(service.Credential, fallbackCredential)
		if credential == "" {
			return blockedResult("credential_required", "升级服务 "+service.Name+" 没有配置凭据名"), nil
		}
		staged, err := s.stageToService(ctx, service, credential, run, channel, bundle, layout, byID)
		if err != nil {
			return blockedResult("update_upload_failed", service.Name+"："+err.Error()), nil
		}
		uploaded += staged.uploaded
		skipped += staged.skipped
		if primaryID == "" {
			primaryID = staged.productReleaseID
		}
		results = append(results, map[string]any{
			"name": service.Name, "url": service.URL, "release_id": staged.productReleaseID,
			"uploaded": staged.uploaded, "skipped": staged.skipped, "releases": staged.releases,
		})
		if _, err := s.Store.UpsertPublication(ctx, releasev2.Publication{
			RunID: run.ID, Provider: "update_service", TargetID: service.Name, State: "staged",
			ExternalID:    staged.productReleaseID,
			ExternalURL:   service.URL + "/v1/admin/releases/" + staged.productReleaseID,
			RequestSHA256: artifact.SHA256,
		}); err != nil {
			return nil, err
		}
	}
	_ = s.Store.AppendEvent(ctx, releasev2.Event{
		RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "update_import.staged",
		Summary: fmt.Sprintf("staged to %d upgrade service(s)", len(services)),
		Detail: map[string]any{
			"channel": channel, "services": results, "uploaded": uploaded, "skipped": skipped,
			"bundle_sha256": artifact.SHA256,
		},
	})
	if run.Status == releasev2.RunVerifying || run.Status == releasev2.RunBuilding {
		if _, err := s.Service.Transition(ctx, run.ID, run.Status, releasev2.RunStaged, "agent:"+p.Token.ID, "upgrade bundle staged"); err == nil {
			run, _ = s.Service.GetRun(ctx, run.ID)
		}
	}
	return textResult(map[string]any{
		"stage": map[string]any{
			"id": primaryID, "state": "staged", "channel": channel,
			"product_version": run.Version, "services": results,
			"uploaded": uploaded, "skipped": skipped,
		},
		"run_status": run.Status,
	}), nil
}

type serviceStage struct {
	productReleaseID string
	releases         []map[string]any
	uploaded         int
	skipped          int
}

// stageToService does the per-service work: ensure components, find or create
// the release, then upload every artifact cell that is missing or different.
func (s *Server) stageToService(ctx context.Context, service updateTarget, credential string, run *releasev2.Run, channel string, bundle releasev2.UpgradeBundle, layout releasev2.BundleLayout, byID map[int64]releasev2.Artifact) (*serviceStage, error) {
	productName := firstNonEmpty(layout.Product, "product")
	names := make([]string, 0, len(bundle.Components))
	for name := range bundle.Components {
		names = append(names, name)
	}
	sort.Strings(names)
	out := &serviceStage{releases: make([]map[string]any, 0, len(names))}
	for _, name := range names {
		component := bundle.Components[name]
		if !isTripleVersion(component.Version) {
			return nil, fmt.Errorf("组件 %s 的版本 %q 不是 MAJOR.MINOR.PATCH；升级服务只接受三段式版本", name, component.Version)
		}
		kind, versioning := "content", "independent"
		if name == productName {
			kind, versioning = "product", "product"
		}
		if _, err := service.Client.EnsureComponent(ctx, credential, releasepublish.EnsureComponentRequest{
			Name: name, DisplayName: name, Kind: kind, Versioning: versioning,
		}); err != nil {
			return nil, fmt.Errorf("登记组件 %s: %w", name, err)
		}
		rel, err := service.Client.FindRelease(ctx, credential, name, channel, component.Version)
		switch {
		case errors.Is(err, releasepublish.ErrNotFoundRemote):
			rel, err = service.Client.CreateRelease(ctx, credential, releasepublish.CreateReleaseRequest{
				Component: name, Version: component.Version, Channel: channel,
			})
			if err != nil {
				return nil, fmt.Errorf("创建发布 %s@%s: %w", name, component.Version, err)
			}
		case err != nil:
			return nil, fmt.Errorf("查询发布 %s@%s: %w", name, component.Version, err)
		}
		if name == productName {
			out.productReleaseID = rel.ID
		}
		existing := map[string]string{}
		if detail, derr := service.Client.GetRelease(ctx, credential, rel.ID); derr == nil {
			for _, uploadedArtifact := range detail.Artifacts {
				existing[artifactCell(uploadedArtifact.Slot, uploadedArtifact.OS, uploadedArtifact.Arch)] = normalizeDigest(uploadedArtifact.SHA256)
			}
		}
		upload := func(slot, platformOS, arch, filename, digest string, artifactID, size int64) error {
			source, ok := byID[artifactID]
			if !ok {
				return fmt.Errorf("升级包引用了未登记的制品 %d", artifactID)
			}
			if strings.TrimSpace(source.StorageKey) == "" {
				return fmt.Errorf("制品 %s 没有存储键", source.Name)
			}
			digest = normalizeDigest(digest)
			cell := artifactCell(slot, platformOS, arch)
			if digest != "" && existing[cell] == digest {
				out.skipped++
				return nil
			}
			reader, err := s.Objects.Open(ctx, source.StorageKey)
			if err != nil {
				return fmt.Errorf("读取制品 %s: %w", source.Name, err)
			}
			if size <= 0 {
				size = source.Size
			}
			_, uploadErr := service.Client.UploadArtifact(ctx, credential, rel.ID, slot, platformOS, arch, filename, digest, size, reader)
			closeErr := reader.Close()
			if uploadErr != nil {
				return fmt.Errorf("上传 %s %s: %w", name, cell, uploadErr)
			}
			if closeErr != nil {
				return fmt.Errorf("关闭制品 %s: %w", source.Name, closeErr)
			}
			existing[cell] = digest
			out.uploaded++
			return nil
		}
		for _, slot := range component.Slots {
			if err := upload(slot.Slot, slot.OS, slot.Arch, slot.Name, slot.SHA256, slot.ArtifactID, slot.Size); err != nil {
				return nil, err
			}
		}
		for _, extra := range component.Artifacts {
			filename, ferr := contentArtifactName(name, component.Version, extra.Name)
			if ferr != nil {
				return nil, ferr
			}
			if err := upload(name, "", "", filename, extra.SHA256, extra.ArtifactID, extra.Size); err != nil {
				return nil, err
			}
		}
		out.releases = append(out.releases, map[string]any{
			"component": name, "version": component.Version, "release_id": rel.ID,
		})
	}
	if out.productReleaseID == "" {
		return nil, fmt.Errorf("升级包里没有 %q 组件", productName)
	}
	return out, nil
}

// getImport reads the staged release on every service plus the current channel
// pointer, so callers can see exactly what each service holds.
func (s *Server) getImport(ctx context.Context, p *principal, args map[string]any) (*toolResult, error) {
	services := s.updateTargets(ctx)
	if len(services) == 0 {
		return blockedResult("update_service_not_configured", "发版设置里还没有配置升级服务地址"), nil
	}
	credential := s.updateCredential(ctx, p.Run.ProjectID, args)
	explicit := stringArg(args, "import_id")
	only := stringArg(args, "service")
	pubs, err := s.Store.ListPublications(ctx, p.Run.ID)
	if err != nil {
		return nil, err
	}
	byService := map[string]string{}
	for _, pub := range pubs {
		if pub.Provider == "update_service" && pub.ExternalID != "" {
			byService[pub.TargetID] = pub.ExternalID
		}
	}
	results := make([]map[string]any, 0, len(services))
	for _, service := range services {
		if only != "" && service.Name != only {
			continue
		}
		releaseID := explicit
		if releaseID == "" {
			releaseID = byService[service.Name]
		}
		if releaseID == "" {
			continue
		}
		cred := firstNonEmpty(service.Credential, credential)
		if cred == "" {
			return blockedResult("credential_required", "升级服务 "+service.Name+" 没有配置凭据名"), nil
		}
		detail, derr := service.Client.GetRelease(ctx, cred, releaseID)
		if derr != nil {
			results = append(results, map[string]any{"name": service.Name, "url": service.URL, "error": derr.Error()})
			continue
		}
		entry := map[string]any{"name": service.Name, "url": service.URL, "release": detail.Release, "artifacts": detail.Artifacts}
		if pointer, perr := service.Client.Latest(ctx, cred, p.Run.Channel); perr == nil {
			entry["channel"] = pointer
		}
		results = append(results, entry)
	}
	if len(results) == 0 {
		return blockedResult("import_missing", "这个运行还没有在升级服务上建过发布"), nil
	}
	return textResult(map[string]any{"services": results}), nil
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

// promote moves the staged pointers on every service to the run channel,
// publishes each channel BOM and reads the signed distribution back before
// reporting success. Repeating it is safe.
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
	staged := map[string]releasev2.Publication{}
	var anyStaged *releasev2.Publication
	for i := range pubs {
		if pubs[i].Provider == "update_service" && pubs[i].ExternalID != "" {
			staged[pubs[i].TargetID] = pubs[i]
			anyStaged = &pubs[i]
		}
	}
	if anyStaged == nil {
		return blockedResult("import_missing", "先执行 update_import_stage 再推进渠道"), nil
	}
	if !strings.EqualFold(anyStaged.State, "promoted") && !strings.EqualFold(anyStaged.State, "published") {
		if profile, _, perr := s.runProfile(ctx, run.ProjectID); perr == nil {
			if required, _ := releasev2.ApprovalPolicy(profile.Release.Policies, run.Channel); required {
				approval, aerr := s.Store.LatestValidApproval(ctx, run.ID, "publish", anyStaged.RequestSHA256)
				if aerr != nil {
					return blockedResult("approval_required", fmt.Sprintf(
						"channel %s requires a human approval bound to bundle %s", run.Channel, anyStaged.RequestSHA256)), nil
				}
				_ = s.Store.AppendEvent(ctx, releasev2.Event{RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "approval.checked",
					Summary: "publish approval verified", Detail: map[string]any{"approval_id": approval.ID, "actor": approval.Actor}})
			}
		}
	}
	services := s.updateTargets(ctx)
	if len(services) == 0 {
		return blockedResult("update_service_not_configured", "发版设置里还没有配置升级服务地址"), nil
	}
	fallbackCredential := s.updateCredential(ctx, run.ProjectID, args)
	outcomes := make([]map[string]any, 0, len(services))
	for _, service := range services {
		credential := firstNonEmpty(service.Credential, fallbackCredential)
		if credential == "" {
			return blockedResult("credential_required", "升级服务 "+service.Name+" 没有配置凭据名"), nil
		}
		if expectedVersion, expectedBOM := stringArg(args, "expected_current_version"), stringArg(args, "expected_bom_sha256"); expectedVersion != "" || expectedBOM != "" {
			pointer, lerr := service.Client.Latest(ctx, credential, run.Channel)
			switch {
			case lerr == nil:
				if expectedVersion != "" && pointer.ProductVersion != expectedVersion {
					return blockedResult("channel_preflight_failed", fmt.Sprintf("%s 渠道 %s 当前是 %s，期望 %s", service.Name, run.Channel, pointer.ProductVersion, expectedVersion)), nil
				}
				if expectedBOM != "" && pointer.BOMSHA256 != expectedBOM {
					return blockedResult("channel_preflight_failed", fmt.Sprintf("%s 渠道 %s 的 BOM 与期望不符", service.Name, run.Channel)), nil
				}
			case errors.Is(lerr, releasepublish.ErrNotFoundRemote):
			default:
				return blockedResult("channel_preflight_failed", service.Name+"：无法读取渠道指针："+lerr.Error()), nil
			}
		}
		outcome, perr := s.publishStaged(ctx, p, service, credential, run.Channel, run)
		if perr != nil {
			_ = s.Store.AppendEvent(ctx, releasev2.Event{RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "channel.promote_failed",
				Summary: "channel promote failed on " + service.Name, Detail: map[string]any{"error": perr.Error()}})
			return blockedResult("channel_promote_failed", service.Name+"："+perr.Error()), nil
		}
		outcomes = append(outcomes, outcome)
		pub := staged[service.Name]
		if _, err := s.Store.UpsertPublication(ctx, releasev2.Publication{
			RunID: run.ID, Provider: "update_service", TargetID: service.Name, State: "published",
			ExternalID: pub.ExternalID, ExternalURL: pub.ExternalURL, RequestSHA256: pub.RequestSHA256,
		}); err != nil {
			return nil, err
		}
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
			_, _ = s.Service.Transition(ctx, run.ID, releasev2.RunPublishing, releasev2.RunPublished, "agent:"+p.Token.ID, "channel promoted and verified")
		}
	}
	if final && run.Status == releasev2.RunPartiallyPublished {
		_ = s.Store.AppendEvent(ctx, releasev2.Event{RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "release.resume",
			Summary: "resuming partial publication", Detail: map[string]any{"from": run.Status}})
		s.finishPublish(ctx, p, run, targets, artifacts, s.githubCredential(ctx, args))
	}
	_ = s.Store.AppendEvent(ctx, releasev2.Event{
		RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "channel.promoted",
		Summary: fmt.Sprintf("channel advanced on %d service(s)", len(outcomes)),
		Detail:  map[string]any{"services": outcomes},
	})
	latest, err := s.Service.GetRun(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	return textResult(map[string]any{"promote": outcomes, "run": latest}), nil
}

// publishStaged is the per-service domain step behind promote.
func (s *Server) publishStaged(ctx context.Context, p *principal, service updateTarget, credential, channel string, run *releasev2.Run) (map[string]any, error) {
	_, raw, err := s.bundleObject(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("读取升级包: %w", err)
	}
	var bundle releasev2.UpgradeBundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return nil, fmt.Errorf("解析升级包: %w", err)
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
	productReleaseID := ""
	for _, name := range names {
		component := bundle.Components[name]
		rel, err := service.Client.FindRelease(ctx, credential, name, channel, component.Version)
		if errors.Is(err, releasepublish.ErrNotFoundRemote) {
			return nil, fmt.Errorf("组件 %s@%s 还没在这个服务上 stage，请重新执行 update_import_stage", name, component.Version)
		}
		if err != nil {
			return nil, fmt.Errorf("查询发布 %s@%s: %w", name, component.Version, err)
		}
		if err := service.Client.PublishRelease(ctx, credential, rel.ID); err != nil {
			return nil, fmt.Errorf("推进 %s@%s: %w", name, component.Version, err)
		}
		bom = append(bom, releasepublish.DistributionComponent{Name: name, Version: component.Version, ReleaseID: rel.ID})
		if name == productName {
			productReleaseID = rel.ID
		}
	}
	if productReleaseID == "" {
		return nil, fmt.Errorf("升级包里没有 %q 组件", productName)
	}
	existing, derr := service.Client.GetDistribution(ctx, credential, channel, run.Version)
	switch {
	case derr == nil && sameDistribution(existing.Components, bom):
	case derr != nil && !errors.Is(derr, releasepublish.ErrNotFoundRemote):
		return nil, fmt.Errorf("读取 BOM %s/%s: %w", channel, run.Version, derr)
	default:
		if _, err := service.Client.PublishDistribution(ctx, credential, releasepublish.PublishDistributionRequest{
			Channel: channel, ProductVersion: run.Version, Components: bom,
		}); err != nil {
			return nil, fmt.Errorf("发布 BOM %s/%s: %w", channel, run.Version, err)
		}
	}
	pointer, err := service.Client.Latest(ctx, credential, channel)
	if err != nil {
		return nil, fmt.Errorf("回读渠道 %s: %w", channel, err)
	}
	if pointer.ProductVersion != run.Version {
		return nil, fmt.Errorf("渠道 %s 上是 %s，期望 %s", channel, pointer.ProductVersion, run.Version)
	}
	if err := compareDistribution(pointer.Components, bom); err != nil {
		return nil, err
	}
	return map[string]any{
		"name": service.Name, "url": service.URL, "state": "published", "channel": channel,
		"product_version": run.Version, "release_id": productReleaseID,
		"bom_sha256": pointer.BOMSHA256, "published_at": pointer.PublishedAt,
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
			mismatches = append(mismatches, fmt.Sprintf("%s=%s（实际 %q）", component.Name, component.Version, byName[component.Name]))
		}
	}
	if len(mismatches) > 0 {
		return fmt.Errorf("渠道内容与 staged 组件不一致：%s", strings.Join(mismatches, ", "))
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

// contentArtifactName enforces the service naming rule for content components.
func contentArtifactName(component, version, filename string) (string, error) {
	expected := fmt.Sprintf("%s-%s.tar.gz", component, version)
	name := strings.TrimSpace(filename)
	if name == expected {
		return name, nil
	}
	if strings.HasSuffix(strings.ToLower(name), ".tar.gz") {
		return expected, nil
	}
	return "", fmt.Errorf("内容制品 %s 必须是 .tar.gz，且命名为 %s", name, expected)
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
