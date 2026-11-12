package releasemcp

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"smartpipe/backend/internal/releasepublish"
	"smartpipe/backend/internal/releasev2"
)

// githubCredential 解析发版用的 GitHub 令牌：工具参数 > 发版设置 > 环境变量 >
// 「GitHub 配置」页保存的全局凭据（PAT 或 OAuth 都存成 github_pat 类型）。
// 顺序说明：控制台已经授权过一次的账号，发版时不该再要求用户配第二遍。
func (s *Server) githubCredential(ctx context.Context, args map[string]any) string {
	if value := firstNonEmpty(
		stringArg(args, "credential"),
		s.settingValue(ctx, "release_github_credential"),
		s.GitHubCredential,
	); value != "" {
		return value
	}
	return s.defaultGitHubCredential(ctx)
}

// defaultGitHubCredential 返回当前启用的全局 GitHub 凭据名；没有时返回空串。
func (s *Server) defaultGitHubCredential(ctx context.Context) string {
	if s.Store == nil {
		return ""
	}
	items, err := s.Store.ListCredentials(ctx, nil)
	if err != nil {
		return ""
	}
	for _, item := range items {
		if item.Kind == "github_pat" && item.ProjectID == nil && item.Enabled {
			return item.Name
		}
	}
	return ""
}

// githubClient 每次按当前设置构造：界面改了 API 地址或凭据后不需要重启进程。
func (s *Server) githubClient(ctx context.Context) *releasepublish.GitHubClient {
	base := s.settingValue(ctx, "release_github_api_base")
	if s.GitHub != nil {
		if base == "" || base == s.GitHub.APIBase {
			if s.GitHub.Creds == nil {
				s.GitHub.Creds = s.credentials()
			}
			return s.GitHub
		}
	}
	client := &releasepublish.GitHubClient{Creds: s.credentials()}
	if base != "" {
		client.APIBase = base
		client.UploadBase = base
	}
	return client
}

// repoSlug extracts owner/repo from the project GitHub URL.
func repoSlug(raw string) (string, string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", "", fmt.Errorf("project has no GitHub repository URL")
	}
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil {
			return "", "", err
		}
		value = parsed.Path
	} else if strings.Contains(value, ":") && !strings.HasPrefix(value, "/") {
		value = strings.SplitN(value, ":", 2)[1]
	}
	value = strings.Trim(strings.TrimSuffix(value, ".git"), "/")
	parts := strings.Split(value, "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("cannot parse owner/repo from %q", raw)
	}
	return parts[len(parts)-2], parts[len(parts)-1], nil
}

// githubRepo 解析 GitHub 发布目标：项目发版配置里显式写的
// publishing.github.owner/repo 优先，其次才是项目仓库 URL。
// 构建仓库和发布仓库分开配置时（例如本地 fixture 构建、GitHub 发布）必须走前者。
func (s *Server) githubRepo(ctx context.Context, projectID string) (string, string, error) {
	if profile, _, err := s.runProfile(ctx, projectID); err == nil && profile != nil && profile.Release != nil {
		if cfg := profile.Release.Publishing["github"]; cfg != nil {
			owner, repo := asString(cfg["owner"]), asString(cfg["repo"])
			if owner != "" && repo != "" {
				return owner, repo, nil
			}
		}
	}
	project, err := s.Store.GetProject(ctx, projectID)
	if err != nil {
		return "", "", err
	}
	return repoSlug(project.GitHubRepoURL)
}

func (s *Server) githubDispatch(ctx context.Context, p *principal, name string, args map[string]any) (*toolResult, bool, error) {
	switch name {
	case "github_draft_create":
		owner, repo, err := s.githubRepo(ctx, p.Run.ProjectID)
		if err != nil {
			return blockedResult("github_repo_missing", err.Error()), true, nil
		}
		run, err := s.Service.GetRun(ctx, p.Run.ID)
		if err != nil {
			return nil, true, err
		}
		tag := firstNonEmpty(stringArg(args, "tag"), run.Tag, "v"+run.Version)
		body := stringArg(args, "body")
		if body == "" {
			body = run.NotesMD
		}
		release, err := s.githubClient(ctx).CreateDraft(ctx, s.githubCredential(ctx, args), owner, repo, releasepublish.CreateDraftRequest{
			TagName: tag, TargetCommitish: run.ResolvedCommit,
			Name:       firstNonEmpty(stringArg(args, "name"), tag),
			Body:       body,
			Prerelease: run.Channel != "stable",
		})
		if err != nil {
			return nil, true, err
		}
		if _, err := s.Store.UpsertPublication(ctx, releasev2.Publication{
			RunID: run.ID, Provider: "github", State: "draft", ExternalID: fmt.Sprint(release.ID),
			ExternalURL: release.HTMLURL,
		}); err != nil {
			return nil, true, err
		}
		_ = s.Store.AppendEvent(ctx, releasev2.Event{RunID: run.ID, Actor: "agent:" + p.Token.ID, Kind: "github.draft_created",
			Summary: "github draft release created", Detail: map[string]any{"release_id": release.ID, "tag": tag}})
		return textResult(release), true, nil

	case "github_asset_upload":
		if s.Objects == nil {
			return blockedResult("storage_not_configured", "release artifact storage is not configured"), true, nil
		}
		owner, repo, err := s.githubRepo(ctx, p.Run.ProjectID)
		if err != nil {
			return blockedResult("github_repo_missing", err.Error()), true, nil
		}
		releaseID := int64Arg(args, "release_id")
		if releaseID == 0 {
			releaseID = s.githubDraftID(ctx, p.Run.ID)
		}
		if releaseID == 0 {
			return blockedResult("github_draft_missing", "create the GitHub draft first"), true, nil
		}
		artifacts, err := s.Store.ListArtifacts(ctx, p.Run.ID)
		if err != nil {
			return nil, true, err
		}
		artifact, pickErr := pickArtifact(artifacts, int64Arg(args, "artifact_id"), stringArg(args, "artifact"), stringArg(args, "role"))
		if pickErr != nil {
			return blockedResult("artifact_missing", pickErr.Error()), true, nil
		}
		if artifact.Role == "upgrade_bundle" {
			return blockedResult("artifact_invalid", "the upgrade bundle is an internal artifact"), true, nil
		}
		// 同名同大小的附件已经存在时直接复用，避免重复调用工具把同一文件传两遍。
		if existing, err := s.githubClient(ctx).GetRelease(ctx, s.githubCredential(ctx, args), owner, repo, releaseID); err == nil {
			for _, asset := range existing.Assets {
				if asset.Name == artifact.Name && asset.Size == artifact.Size {
					return textResult(asset), true, nil
				}
			}
		}
		reader, err := s.Objects.Open(ctx, artifact.StorageKey)
		if err != nil {
			return nil, true, err
		}
		defer reader.Close()
		asset, err := s.githubClient(ctx).UploadAsset(ctx, s.githubCredential(ctx, args), owner, repo, releaseID,
			artifact.Name, artifact.ContentType, reader, artifact.Size)
		if err != nil {
			return nil, true, err
		}
		if asset.Digest != "" && !sameDigest(asset.Digest, artifact.SHA256) {
			return blockedResult("digest_mismatch", fmt.Sprintf("github digest %s != local %s", asset.Digest, artifact.SHA256)), true, nil
		}
		_ = s.Store.AppendEvent(ctx, releasev2.Event{RunID: p.Run.ID, Actor: "agent:" + p.Token.ID, Kind: "github.asset_uploaded",
			Summary: artifact.Name, Detail: map[string]any{"asset_id": asset.ID, "size": asset.Size, "digest": asset.Digest}})
		return textResult(asset), true, nil

	case "github_release_publish":
		owner, repo, err := s.githubRepo(ctx, p.Run.ProjectID)
		if err != nil {
			return blockedResult("github_repo_missing", err.Error()), true, nil
		}
		releaseID := int64Arg(args, "release_id")
		if releaseID == 0 {
			releaseID = s.githubDraftID(ctx, p.Run.ID)
		}
		if releaseID == 0 {
			return blockedResult("github_draft_missing", "create the GitHub draft first"), true, nil
		}
		release, err := s.githubClient(ctx).PublishRelease(ctx, s.githubCredential(ctx, args), owner, repo, releaseID)
		if err != nil {
			return nil, true, err
		}
		if _, err := s.Store.UpsertPublication(ctx, releasev2.Publication{
			RunID: p.Run.ID, Provider: "github", State: "published", ExternalID: fmt.Sprint(release.ID), ExternalURL: release.HTMLURL,
		}); err != nil {
			return nil, true, err
		}
		_ = s.Store.AppendEvent(ctx, releasev2.Event{RunID: p.Run.ID, Actor: "agent:" + p.Token.ID, Kind: "github.published",
			Summary: "github release published", Detail: map[string]any{"url": release.HTMLURL, "assets": len(release.Assets)}})
		return textResult(release), true, nil

	case "github_release_get":
		owner, repo, err := s.githubRepo(ctx, p.Run.ProjectID)
		if err != nil {
			return blockedResult("github_repo_missing", err.Error()), true, nil
		}
		releaseID := int64Arg(args, "release_id")
		if releaseID == 0 {
			releaseID = s.githubDraftID(ctx, p.Run.ID)
		}
		release, err := s.githubClient(ctx).GetRelease(ctx, s.githubCredential(ctx, args), owner, repo, releaseID)
		if err != nil {
			return nil, true, err
		}
		return textResult(release), true, nil
	}
	return nil, false, nil
}

func (s *Server) githubDraftID(ctx context.Context, runID string) int64 {
	pubs, err := s.Store.ListPublications(ctx, runID)
	if err != nil {
		return 0
	}
	for _, pub := range pubs {
		if pub.Provider == "github" && pub.ExternalID != "" {
			var id int64
			_, _ = fmt.Sscan(pub.ExternalID, &id)
			return id
		}
	}
	return 0
}

// verifyGitHubAssets compares draft asset digests with registered artifacts.
func verifyGitHubAssets(release *releasepublish.GitHubRelease, artifacts []releasev2.Artifact) []string {
	if release == nil {
		return []string{"release not found"}
	}
	uploaded := map[string]releasepublish.GitHubAsset{}
	for _, asset := range release.Assets {
		uploaded[asset.Name] = asset
	}
	var missing []string
	for _, artifact := range artifacts {
		if artifact.Role == "upgrade_bundle" {
			continue
		}
		asset, ok := uploaded[artifact.Name]
		if !ok {
			missing = append(missing, "missing asset "+artifact.Name)
			continue
		}
		switch {
		case asset.Digest != "":
			if !sameDigest(asset.Digest, artifact.SHA256) {
				missing = append(missing, "digest mismatch "+artifact.Name)
			}
		case artifact.Size > 0 && asset.Size != artifact.Size:
			// 老版本 GitHub 响应没有 digest，退化成大小核对；两者都没有就只能算通过。
			missing = append(missing, fmt.Sprintf("size mismatch %s: github %d != local %d", artifact.Name, asset.Size, artifact.Size))
		}
	}
	sort.Strings(missing)
	return missing
}

// sameDigest 容忍 GitHub 返回带/不带 sha256: 前缀的十六进制摘要。
func sameDigest(left, right string) bool {
	normalize := func(value string) string {
		value = strings.ToLower(strings.TrimSpace(value))
		return strings.TrimPrefix(value, "sha256:")
	}
	return normalize(left) == normalize(right) && normalize(left) != ""
}

// pickArtifact 按 id、文件名或角色挑出一个制品；不唯一时给出候选，避免静默选错。
func pickArtifact(artifacts []releasev2.Artifact, id int64, name, role string) (*releasev2.Artifact, error) {
	if id != 0 {
		for i := range artifacts {
			if artifacts[i].ID == id {
				return &artifacts[i], nil
			}
		}
		return nil, fmt.Errorf("artifact_id %d does not belong to this run", id)
	}
	var matched []releasev2.Artifact
	for _, artifact := range artifacts {
		switch {
		case name != "" && artifact.Name == name:
			matched = append(matched, artifact)
		case role != "" && artifact.Role == role:
			matched = append(matched, artifact)
		}
	}
	switch len(matched) {
	case 1:
		return &matched[0], nil
	case 0:
		if name == "" && role == "" {
			return nil, fmt.Errorf("pass artifact_id, artifact (file name) or role")
		}
		return nil, fmt.Errorf("no artifact matches name=%q role=%q", name, role)
	default:
		names := make([]string, 0, len(matched))
		for _, artifact := range matched {
			names = append(names, fmt.Sprintf("%d:%s", artifact.ID, artifact.Name))
		}
		sort.Strings(names)
		return nil, fmt.Errorf("artifact is ambiguous, pass artifact_id: %s", strings.Join(names, ", "))
	}
}
