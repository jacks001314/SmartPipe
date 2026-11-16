package releasemcp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"smartpipe/backend/internal/releasepublish"
	"smartpipe/backend/internal/releasev2"
)

// deliversTo reports whether an artifact declares the given delivery
// destination (github, update:full, update:thin, update:manifest).
func deliversTo(artifact releasev2.Artifact, destination string) bool {
	for _, item := range artifact.Destinations {
		if strings.EqualFold(strings.TrimSpace(item), destination) {
			return true
		}
	}
	return false
}

// githubPublishOptions keeps GitHub's "Latest" badge on the stable line.
//
// A tag can be created by a beta run first (prerelease=true) and reused by the
// stable run later; GitHub only moves Latest when the published release is not
// a prerelease and make_latest says so. Non-stable runs must not demote an
// already published stable release, so they only force prerelease on drafts.
func githubPublishOptions(run *releasev2.Run, release *releasepublish.GitHubRelease) releasepublish.PublishOptions {
	if run != nil && run.Channel == "stable" {
		prerelease := false
		return releasepublish.PublishOptions{Prerelease: &prerelease, MakeLatest: "true"}
	}
	if release != nil && release.Draft {
		prerelease := true
		return releasepublish.PublishOptions{Prerelease: &prerelease, MakeLatest: "false"}
	}
	return releasepublish.PublishOptions{}
}

// githubStaleAssets 返回“属于本次版本、但本次发布不再产出”的旧资产。
// 只按版本号过滤，避免清掉 Release 上与本次版本无关的文件。
func githubStaleAssets(release *releasepublish.GitHubRelease, artifacts []releasev2.Artifact, version string) []releasepublish.GitHubAsset {
	if release == nil || strings.TrimSpace(version) == "" {
		return nil
	}
	expected := make(map[string]bool, len(artifacts))
	for _, artifact := range artifacts {
		if deliversTo(artifact, "github") {
			expected[artifact.Name] = true
		}
	}
	var stale []releasepublish.GitHubAsset
	for _, asset := range release.Assets {
		if expected[asset.Name] {
			continue
		}
		if !strings.Contains(asset.Name, version) {
			continue
		}
		stale = append(stale, asset)
	}
	return stale
}

// githubSync drives a full GitHub release without any human step: it reuses or
// creates the draft, uploads every verified artifact, verifies name/size/digest,
// publishes, and reads the release back one more time.
func (s *Server) githubSync(ctx context.Context, p *principal, args map[string]any) (*toolResult, error) {
	if s.Objects == nil {
		return blockedResult("storage_not_configured", "release artifact storage is not configured"), nil
	}
	run, targets, artifacts, err := s.runSnapshot(ctx, p)
	if err != nil {
		return nil, err
	}
	if err := s.Store.CheckDesktopReleaseGate(ctx, run, targets, artifacts); err != nil {
		return blockedResult("online_acceptance_required", err.Error()), nil
	}
	owner, repo, err := s.githubRepo(ctx, run.ProjectID)
	if err != nil {
		return blockedResult("github_repo_missing", err.Error()), nil
	}
	credential, err := s.githubCredentialForRepo(ctx, owner, repo, args)
	if err != nil {
		return blockedResult("github_credential_unavailable", err.Error()), nil
	}
	client := s.githubClient(ctx)
	tag := firstNonEmpty(stringArg(args, "tag"), run.Tag, "v"+run.Version)
	releaseID := int64Arg(args, "release_id")
	if releaseID == 0 {
		releaseID = s.githubDraftID(ctx, run.ID)
	}

	var release *releasepublish.GitHubRelease
	if releaseID != 0 {
		release, err = client.GetRelease(ctx, credential, owner, repo, releaseID)
		switch {
		case errors.Is(err, releasepublish.ErrNotFoundRemote):
			// 记录的草稿已被删除（或远端数据被重置）：按新建处理，而不是让整条
			// 发布链卡在一个不存在的外部 id 上。
			release = nil
		case err != nil:
			return nil, err
		}
	}
	if release == nil {
		// 同版本重发很常见（修一个打包问题再发一次）：远端可能已经有这个
		// tag 的 Release，此时直接复用并按摘要替换资产，不能再建撞名草稿。
		release, err = client.GetReleaseByTag(ctx, credential, owner, repo, tag)
		switch {
		case err == nil:
			releaseID = release.ID
			state := "published"
			if release.Draft {
				state = "draft"
			}
			if _, uerr := s.Store.UpsertPublication(ctx, releasev2.Publication{
				RunID: run.ID, Provider: "github", State: state, ExternalID: fmt.Sprint(release.ID), ExternalURL: release.HTMLURL,
			}); uerr != nil {
				return nil, uerr
			}
			_ = s.Store.AppendEvent(ctx, releasev2.Event{RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "github.release_reused",
				Summary: "existing github release reused for tag", Detail: map[string]any{"release_id": release.ID, "tag": tag, "draft": release.Draft}})
		case errors.Is(err, releasepublish.ErrNotFoundRemote):
			body := firstNonEmpty(stringArg(args, "body"), run.NotesMD)
			release, err = client.CreateDraft(ctx, credential, owner, repo, releasepublish.CreateDraftRequest{
				TagName: tag, TargetCommitish: run.ResolvedCommit,
				Name: firstNonEmpty(stringArg(args, "name"), tag), Body: body,
				Prerelease: run.Channel != "stable",
			})
			if err != nil {
				return nil, err
			}
			releaseID = release.ID
			if _, uerr := s.Store.UpsertPublication(ctx, releasev2.Publication{
				RunID: run.ID, Provider: "github", State: "draft", ExternalID: fmt.Sprint(release.ID), ExternalURL: release.HTMLURL,
			}); uerr != nil {
				return nil, uerr
			}
			_ = s.Store.AppendEvent(ctx, releasev2.Event{RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "github.draft_created",
				Summary: "github draft release created", Detail: map[string]any{"release_id": release.ID, "tag": tag}})
		default:
			return nil, err
		}
	}

	// 同版本重发：旧构建的资产名可能带不同日期（如 …-0.5.17.2026-11-14.exe），
	// 直接复用 Release 会留下两套同版本安装包。只清理名字里含本次版本号、
	// 且不在本次制品清单里的旧资产，其余资产不动。
	if stale := githubStaleAssets(release, artifacts, run.Version); len(stale) > 0 {
		for _, asset := range stale {
			if err := client.DeleteAsset(ctx, credential, owner, repo, asset.ID); err != nil {
				return nil, err
			}
			_ = s.Store.AppendEvent(ctx, releasev2.Event{RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "github.asset_removed",
				Summary: asset.Name, Detail: map[string]any{"asset_id": asset.ID, "reason": "stale same-version asset", "digest": asset.Digest}})
		}
		release, err = client.GetRelease(ctx, credential, owner, repo, releaseID)
		if err != nil {
			return nil, err
		}
	}
	existing := map[string]releasepublish.GitHubAsset{}
	for _, asset := range release.Assets {
		existing[asset.Name] = asset
	}
	uploaded := make([]map[string]any, 0, len(artifacts))
	skipped := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		// GitHub 只收 destinations 里写明 github 的制品。两个平台的
		// update-bundle.json 同名，硬传上去会互相覆盖/撞名；它本来也只服务于
		// 升级服务（update:manifest）。
		if !deliversTo(artifact, "github") {
			continue
		}
		if asset, ok := existing[artifact.Name]; ok && asset.Size == artifact.Size && (asset.Digest == "" || sameDigest(asset.Digest, artifact.SHA256)) {
			skipped = append(skipped, artifact.Name)
			continue
		}
		// 同名但摘要不同的旧资产必须先删：GitHub 已发布的 Release 不能覆盖同名附件。
		if asset, ok := existing[artifact.Name]; ok {
			if err := client.DeleteAsset(ctx, credential, owner, repo, asset.ID); err != nil {
				return nil, err
			}
			_ = s.Store.AppendEvent(ctx, releasev2.Event{RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "github.asset_replaced",
				Summary: artifact.Name, Detail: map[string]any{"asset_id": asset.ID, "old_digest": asset.Digest, "new_digest": artifact.SHA256}})
			delete(existing, artifact.Name)
		}
		reader, err := s.Objects.Open(ctx, artifact.StorageKey)
		if err != nil {
			return nil, err
		}
		asset, uploadErr := client.UploadAsset(ctx, credential, owner, repo, releaseID,
			artifact.Name, artifact.ContentType, reader, artifact.Size)
		closeErr := reader.Close()
		if uploadErr != nil {
			return nil, uploadErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if asset.Digest != "" && !sameDigest(asset.Digest, artifact.SHA256) {
			return blockedResult("digest_mismatch", fmt.Sprintf("%s: github digest %s != local %s", artifact.Name, asset.Digest, artifact.SHA256)), nil
		}
		uploaded = append(uploaded, map[string]any{"name": asset.Name, "size": asset.Size, "digest": asset.Digest})
		_ = s.Store.AppendEvent(ctx, releasev2.Event{RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "github.asset_uploaded",
			Summary: artifact.Name, Detail: map[string]any{"asset_id": asset.ID, "size": asset.Size, "digest": asset.Digest}})
	}

	after, err := client.GetRelease(ctx, credential, owner, repo, releaseID)
	if err != nil {
		return nil, err
	}
	if gaps := verifyGitHubAssets(after, artifacts); len(gaps) > 0 {
		return blockedResult("github_assets_incomplete", fmt.Sprintf("上传后校验未通过：%v", gaps)), nil
	}
	if _, err := client.UpdateRelease(ctx, credential, owner, repo, releaseID, githubPublishOptions(run, after)); err != nil {
		return nil, err
	}
	final, err := client.GetRelease(ctx, credential, owner, repo, releaseID)
	if err != nil {
		return nil, err
	}
	if gaps := verifyGitHubAssets(final, artifacts); len(gaps) > 0 {
		return blockedResult("github_publish_unverified", fmt.Sprintf("发布后回读不一致：%v", gaps)), nil
	}
	if _, err := s.Store.UpsertPublication(ctx, releasev2.Publication{
		RunID: run.ID, Provider: "github", State: "published", ExternalID: fmt.Sprint(final.ID), ExternalURL: final.HTMLURL,
	}); err != nil {
		return nil, err
	}
	digests := make([]map[string]any, 0, len(final.Assets))
	for _, asset := range final.Assets {
		digests = append(digests, map[string]any{"name": asset.Name, "size": asset.Size, "digest": asset.Digest})
	}
	sort.Slice(digests, func(i, j int) bool { return digests[i]["name"].(string) < digests[j]["name"].(string) })
	_ = s.Store.AppendEvent(ctx, releasev2.Event{RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "github.published",
		Summary: "github release published and verified", Detail: map[string]any{
			"url": final.HTMLURL, "assets": len(final.Assets), "uploaded": len(uploaded), "skipped": skipped, "digests": digests,
		}})
	return textResult(map[string]any{
		"release": final, "uploaded": uploaded, "skipped": skipped,
		"assets": len(final.Assets), "digests": digests,
	}), nil
}
