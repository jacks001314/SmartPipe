package releasepublish

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GitHubRelease is the subset of the GitHub release payload the release plane
// depends on.
type GitHubRelease struct {
	ID         int64         `json:"id"`
	TagName    string        `json:"tag_name"`
	Name       string        `json:"name"`
	Draft      bool          `json:"draft"`
	Prerelease bool          `json:"prerelease"`
	HTMLURL    string        `json:"html_url"`
	UploadURL  string        `json:"upload_url"`
	Assets     []GitHubAsset `json:"assets,omitempty"`
}

type GitHubAsset struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	State      string `json:"state"`
	Digest     string `json:"digest,omitempty"`
	BrowserURL string `json:"browser_download_url,omitempty"`
}

// GitHubClient is a minimal typed client for release assets. Uploads stream
// from an io.Reader; callers must supply the exact byte length.
type GitHubClient struct {
	APIBase    string
	UploadBase string
	Creds      Provider
	HTTP       *http.Client
}

func (c *GitHubClient) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Minute}
}

func (c *GitHubClient) apiBase() string {
	if strings.TrimSpace(c.APIBase) == "" {
		return "https://api.github.com"
	}
	return strings.TrimRight(c.APIBase, "/")
}

func (c *GitHubClient) uploadBase() string {
	if strings.TrimSpace(c.UploadBase) != "" {
		return strings.TrimRight(c.UploadBase, "/")
	}
	if strings.Contains(c.apiBase(), "api.github.com") {
		return "https://uploads.github.com"
	}
	return c.apiBase()
}

type CreateDraftRequest struct {
	TagName         string `json:"tag_name"`
	TargetCommitish string `json:"target_commitish,omitempty"`
	Name            string `json:"name,omitempty"`
	Body            string `json:"body,omitempty"`
	Draft           bool   `json:"draft"`
	Prerelease      bool   `json:"prerelease"`
}

func (c *GitHubClient) CreateDraft(ctx context.Context, credentialRef, owner, repo string, in CreateDraftRequest) (*GitHubRelease, error) {
	if strings.TrimSpace(in.TagName) == "" {
		return nil, fmt.Errorf("%w: tag_name is required", ErrInvalid)
	}
	in.Draft = true
	var out GitHubRelease
	if err := c.doJSON(ctx, credentialRef, http.MethodPost, "/repos/"+owner+"/"+repo+"/releases", in, &out); err != nil {
		return nil, err
	}
	if out.ID == 0 {
		return nil, fmt.Errorf("%w: github returned no release id", ErrRemote)
	}
	return &out, nil
}

func (c *GitHubClient) GetRelease(ctx context.Context, credentialRef, owner, repo string, releaseID int64) (*GitHubRelease, error) {
	var out GitHubRelease
	if err := c.doJSON(ctx, credentialRef, http.MethodGet,
		fmt.Sprintf("/repos/%s/%s/releases/%d", owner, repo, releaseID), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UploadAsset streams an artifact into an existing draft release.
func (c *GitHubClient) UploadAsset(ctx context.Context, credentialRef, owner, repo string, releaseID int64, name, contentType string, body io.Reader, size int64) (*GitHubAsset, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("%w: asset name is required", ErrInvalid)
	}
	if size <= 0 {
		return nil, fmt.Errorf("%w: asset size must be positive", ErrInvalid)
	}
	secret, err := c.resolve(ctx, credentialRef)
	if err != nil {
		return nil, err
	}
	target := fmt.Sprintf("%s/repos/%s/%s/releases/%d/assets?name=%s",
		c.uploadBase(), url.PathEscape(owner), url.PathEscape(repo), releaseID, url.QueryEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = size
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/octet-stream"
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: upload asset: %v", ErrRemote, err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnprocessableEntity {
		return nil, fmt.Errorf("%w: %s", ErrConflictRemote, remoteMessage(payload, resp.Status))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: %s", ErrRemote, remoteMessage(payload, resp.Status))
	}
	var asset GitHubAsset
	if err := json.Unmarshal(payload, &asset); err != nil {
		return nil, fmt.Errorf("%w: decode asset: %v", ErrRemote, err)
	}
	return &asset, nil
}

func (c *GitHubClient) PublishRelease(ctx context.Context, credentialRef, owner, repo string, releaseID int64) (*GitHubRelease, error) {
	var out GitHubRelease
	body := map[string]any{"draft": false}
	if err := c.doJSON(ctx, credentialRef, http.MethodPatch,
		fmt.Sprintf("/repos/%s/%s/releases/%d", owner, repo, releaseID), body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *GitHubClient) resolve(ctx context.Context, ref string) (string, error) {
	if c.Creds == nil {
		return "", fmt.Errorf("%w: no credential provider", ErrCredentialMissing)
	}
	return c.Creds.Resolve(ctx, ref)
}

func (c *GitHubClient) doJSON(ctx context.Context, ref, method, path string, body any, out any) error {
	secret, err := c.resolve(ctx, ref)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.apiBase()+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRemote, err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusUnprocessableEntity {
		return fmt.Errorf("%w: %s", ErrConflictRemote, remoteMessage(payload, resp.Status))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: %s", ErrRemote, remoteMessage(payload, resp.Status))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(payload, out)
}
