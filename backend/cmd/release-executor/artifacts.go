package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"smartpipe/backend/internal/releasev2"
)

type artifactBegin struct {
	UploadURL    string            `json:"upload_url"`
	Method       string            `json:"method"`
	Headers      map[string]string `json:"headers"`
	StorageKey   string            `json:"storage_key"`
	ContentType  string            `json:"content_type"`
	Direct       bool              `json:"direct"`
	Kind         string            `json:"kind,omitempty"`
	Path         string            `json:"path,omitempty"`
	OS           string            `json:"os,omitempty"`
	Arch         string            `json:"arch,omitempty"`
	Destinations []string          `json:"destinations,omitempty"`
}

// artifactHTTPClient 专用于制品传输。http.Client.Timeout 会把上传 body 与
// 响应体读取一起计时，1 GB 级的安装包不可能在 30 秒内传完；这里只保留
// 连接级超时，整体时长交给请求 context 控制。
var artifactHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
		MaxIdleConnsPerHost:   4,
	},
}

// uploadArtifacts collects every declared artifact for the target, streams it
// to the release-service artifact endpoint, and returns the authoritative
// manifest for this target.
func uploadArtifacts(ctx context.Context, client *http.Client, cfg config, env *targetEnvelope, repoDir string, out io.Writer) (*releasev2.ArtifactManifest, error) {
	if env.Target == nil || env.Run == nil {
		return nil, fmt.Errorf("invalid target envelope")
	}
	manifest := &releasev2.ArtifactManifest{
		Schema: releasev2.ArtifactManifestSchema,
		RunID:  env.Run.ID, TargetID: env.Target.TargetID,
		OS: env.Target.OS, Arch: env.Target.Arch,
		Version: env.Run.Version, BuildVersion: env.Run.BuildVersion,
		Commit: env.Run.ResolvedCommit, GeneratedAt: time.Now().UTC(),
	}
	if len(env.Target.Artifacts) == 0 {
		manifest.Normalize()
		return manifest, nil
	}
	for _, rule := range env.Target.Artifacts {
		matches, err := expandArtifactGlob(repoDir, rule.Glob)
		if err != nil {
			return nil, fmt.Errorf("artifact %s glob: %w", rule.Role, err)
		}
		if len(matches) == 0 {
			if rule.Required {
				return nil, fmt.Errorf("artifact_missing:%s matched no files for %s", rule.Role, rule.Glob)
			}
			continue
		}
		for _, file := range matches {
			item, err := uploadArtifactFile(ctx, client, cfg, env, rule, file, repoDir, out)
			if err != nil {
				return nil, err
			}
			manifest.Artifacts = append(manifest.Artifacts, *item)
		}
	}
	manifest.Normalize()
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("artifact manifest: %w", err)
	}
	return manifest, nil
}

// uploadManifest reports the authoritative artifact list for this target.
// release-service stores it, then merges every target manifest into the run
// level manifest used by upgrade import, channels and GitHub publishing.
func uploadManifest(ctx context.Context, client *http.Client, cfg config, env *targetEnvelope, manifest *releasev2.ArtifactManifest) error {
	if manifest == nil {
		return fmt.Errorf("artifact manifest is empty")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := postJSON(ctx, client, cfg,
		"/internal/release-executors/"+cfg.ExecutorID+"/manifest", manifest, nil); err != nil {
		return fmt.Errorf("upload artifact manifest: %w", err)
	}
	return nil
}

func uploadArtifactFile(ctx context.Context, client *http.Client, cfg config, env *targetEnvelope, rule releasev2.ArtifactRule, file, repoDir string, out io.Writer) (*releasev2.ManifestArtifact, error) {
	info, err := os.Stat(file)
	if err != nil {
		return nil, fmt.Errorf("stat artifact: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("artifact %s is not a regular file", file)
	}
	relPath, err := filepath.Rel(repoDir, file)
	if err != nil || relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("artifact %s is outside source root", file)
	}
	relPath = filepath.ToSlash(relPath)
	kind := firstNonEmpty(rule.Kind, rule.Role)
	destinations := releasev2.DefaultDestinations(rule.Kind, rule.Destinations)
	// 制品平台：规则里显式声明优先（交叉构建），否则用 target 平台。
	artifactOS := firstNonEmpty(rule.OS, env.Target.OS)
	artifactArch := firstNonEmpty(rule.Arch, env.Target.Arch)
	begin := map[string]any{
		"run_id": env.Run.ID, "target_id": env.Target.TargetID, "attempt": env.Target.Attempt,
		"role": rule.Role, "name": filepath.Base(file), "size": info.Size(),
		"content_type": firstNonEmpty(rule.ContentType, "application/octet-stream"),
		"kind":         kind, "path": relPath, "os": artifactOS, "arch": artifactArch,
		"destinations": destinations,
	}
	raw, _ := json.Marshal(begin)
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		strings.TrimRight(cfg.ServiceURL, "/")+"/internal/release-executors/"+cfg.ExecutorID+"/artifacts/begin", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("begin artifact upload: %w", err)
	}
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("begin artifact upload: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	var ticket artifactBegin
	if err := json.Unmarshal(payload, &ticket); err != nil {
		return nil, fmt.Errorf("decode artifact ticket: %w", err)
	}
	uploadURL, err := resolveURL(cfg.ServiceURL, ticket.UploadURL)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	hash := sha256.New()
	body := io.TeeReader(f, hash)

	putCtx, putCancel := context.WithTimeout(ctx, 6*time.Hour)
	defer putCancel()
	method := ticket.Method
	if method == "" {
		method = http.MethodPut
	}
	putReq, err := http.NewRequestWithContext(putCtx, method, uploadURL, body)
	if err != nil {
		return nil, err
	}
	putReq.ContentLength = info.Size()
	for k, v := range ticket.Headers {
		putReq.Header.Set(k, v)
	}
	contentType := firstNonEmpty(ticket.ContentType, rule.ContentType, "application/octet-stream")
	putReq.Header.Set("Content-Type", contentType)
	if cfg.Token != "" && strings.HasPrefix(uploadURL, strings.TrimRight(cfg.ServiceURL, "/")) {
		putReq.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	putResp, err := artifactHTTPClient.Do(putReq)
	if err != nil {
		return nil, fmt.Errorf("upload artifact %s: %w", filepath.Base(file), err)
	}
	putPayload, _ := io.ReadAll(io.LimitReader(putResp.Body, 1<<20))
	putResp.Body.Close()
	if putResp.StatusCode < 200 || putResp.StatusCode >= 300 {
		return nil, fmt.Errorf("upload artifact %s: HTTP %d: %s", filepath.Base(file), putResp.StatusCode, strings.TrimSpace(string(putPayload)))
	}
	localDigest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	var artifact releasev2.Artifact
	if ticket.Direct {
		confirmed, err := confirmArtifactUpload(ctx, artifactHTTPClient, cfg, env, rule, filepath.Base(file), relPath, info.Size(), contentType, localDigest)
		if err != nil {
			return nil, err
		}
		artifact = *confirmed
	} else if err := json.Unmarshal(putPayload, &artifact); err != nil {
		return nil, fmt.Errorf("decode artifact: %w", err)
	}
	if artifact.SHA256 != localDigest {
		return nil, fmt.Errorf("artifact %s digest mismatch: server %s local %s", filepath.Base(file), artifact.SHA256, localDigest)
	}
	if artifact.Size != info.Size() {
		return nil, fmt.Errorf("artifact %s size mismatch: server %d local %d", filepath.Base(file), artifact.Size, info.Size())
	}
	fmt.Fprintf(out, "uploaded %s role=%s size=%d sha256=%s\n", artifact.Name, artifact.Role, artifact.Size, artifact.SHA256)
	return &releasev2.ManifestArtifact{
		Role: rule.Role, Kind: kind, Path: relPath, Name: artifact.Name,
		OS: artifactOS, Arch: artifactArch,
		Size: artifact.Size, SHA256: artifact.SHA256, ContentType: artifact.ContentType,
		Required: rule.Required, Destinations: destinations,
	}, nil
}

// expandArtifactGlob supports plain filepath globs plus a single ** segment.
func expandArtifactGlob(root, pattern string) ([]string, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, nil
	}
	abs := pattern
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, filepath.FromSlash(pattern))
	}
	if !strings.Contains(pattern, "**") {
		return filepath.Glob(abs)
	}
	parts := strings.SplitN(pattern, "**", 2)
	base := filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(parts[0], "/")))
	suffix := strings.TrimPrefix(parts[1], "/")
	var out []string
	err := filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return nil
		}
		// ** 需要跨目录匹配：从最深的目录开始逐级尝试后缀，命中最浅的一处即可。
		segments := strings.Split(filepath.ToSlash(rel), "/")
		for i := 0; i < len(segments); i++ {
			ok, err := filepath.Match(filepath.FromSlash(suffix), filepath.FromSlash(strings.Join(segments[i:], "/")))
			if err != nil {
				return err
			}
			if ok {
				out = append(out, p)
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func resolveURL(base, ref string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return "", err
	}
	if parsed.IsAbs() {
		return parsed.String(), nil
	}
	b, err := url.Parse(strings.TrimRight(base, "/") + "/")
	if err != nil {
		return "", err
	}
	return b.ResolveReference(parsed).String(), nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// confirmArtifactUpload tells release-service that a pre-signed PUT finished so
// it can re-hash the object and register the artifact row.
func confirmArtifactUpload(ctx context.Context, client *http.Client, cfg config, env *targetEnvelope, rule releasev2.ArtifactRule, name, relPath string, size int64, contentType, digest string) (*releasev2.Artifact, error) {
	body := map[string]any{
		"run_id": env.Run.ID, "target_id": env.Target.TargetID, "attempt": env.Target.Attempt,
		"role": rule.Role, "name": name, "size": size, "content_type": contentType, "sha256": digest,
		"kind": firstNonEmpty(rule.Kind, rule.Role), "path": relPath,
		"os": firstNonEmpty(rule.OS, env.Target.OS), "arch": firstNonEmpty(rule.Arch, env.Target.Arch),
		"destinations": releasev2.DefaultDestinations(rule.Kind, rule.Destinations),
	}
	raw, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(cfg.ServiceURL, "/")+"/internal/release-executors/"+cfg.ExecutorID+"/artifacts/confirm", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("confirm artifact upload: %w", err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("confirm artifact upload: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	var artifact releasev2.Artifact
	if err := json.Unmarshal(payload, &artifact); err != nil {
		return nil, fmt.Errorf("decode confirmed artifact: %w", err)
	}
	return &artifact, nil
}
