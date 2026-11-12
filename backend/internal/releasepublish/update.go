package releasepublish

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

var (
	ErrInvalid           = errors.New("releasepublish: invalid request")
	ErrCredentialMissing = errors.New("releasepublish: credential missing")
	ErrRemote            = errors.New("releasepublish: remote call failed")
	ErrConflictRemote    = errors.New("releasepublish: remote state conflict")
	ErrNotFoundRemote    = errors.New("releasepublish: remote object not found")
)

// maxResponseBytes caps admin metadata responses; artifact uploads never go
// through this path.
const maxResponseBytes = 8 << 20

// The update service addressed here is the syntropy_update server: components
// own releases, releases own content-addressed artifacts, and a distribution
// (BOM) pins one release per component for a product version on a channel.
// SmartPipe stages by creating releases and uploading bytes, and promotes by
// moving each component channel pointer and then publishing the BOM.

// Component is a managed component definition on the update service.
type Component struct {
	Name              string `json:"name"`
	DisplayName       string `json:"displayName,omitempty"`
	Kind              string `json:"kind,omitempty"`
	Versioning        string `json:"versioning,omitempty"`
	MinProductVersion string `json:"minProductVersion,omitempty"`
}

// EnsureComponentRequest registers a component. The service treats this as
// idempotent and keeps the first definition it stored.
type EnsureComponentRequest struct {
	Name              string
	DisplayName       string
	Kind              string
	Versioning        string
	MinProductVersion string
}

// Release is one component release on one channel.
type Release struct {
	ID                      string `json:"id"`
	Component               string `json:"component"`
	Version                 string `json:"version"`
	Channel                 string `json:"channel"`
	Notes                   string `json:"notes,omitempty"`
	NotesURL                string `json:"notesUrl,omitempty"`
	MinimumSupportedVersion string `json:"minimumSupportedVersion,omitempty"`
	PublishedAtMS           int64  `json:"publishedAtMs"`
	CreatedAtMS             int64  `json:"createdAtMs"`
	Yanked                  bool   `json:"yanked"`
}

// Artifact is one uploaded artifact slot of a release.
type Artifact struct {
	ID        string `json:"id"`
	ReleaseID string `json:"releaseId"`
	Slot      string `json:"slot"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Filename  string `json:"filename"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
}

// ReleaseDetail is the admin read of one release with its artifacts.
type ReleaseDetail struct {
	Release   Release    `json:"release"`
	Artifacts []Artifact `json:"artifacts"`
}

// CreateReleaseRequest creates a release without moving the channel pointer.
type CreateReleaseRequest struct {
	Component               string
	Version                 string
	Channel                 string
	Notes                   string
	NotesURL                string
	MinimumSupportedVersion string
}

// DistributionComponent pins one component version in a BOM.
type DistributionComponent struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	ReleaseID string `json:"releaseId,omitempty"`
}

// Distribution is the BOM of one product version on one channel.
type Distribution struct {
	Channel        string                  `json:"channel"`
	ProductVersion string                  `json:"productVersion"`
	Components     []DistributionComponent `json:"components"`
	PublishedAtMS  int64                   `json:"publishedAtMs"`
	CreatedAtMS    int64                   `json:"createdAtMs"`
}

// PublishDistributionRequest publishes (or advances) a channel BOM.
type PublishDistributionRequest struct {
	Channel        string
	ProductVersion string
	Components     []DistributionComponent
}

// ChannelPointer is what a client sees for a channel: the product version and
// the component set of the published distribution.
type ChannelPointer struct {
	Channel        string                  `json:"channel"`
	ProductVersion string                  `json:"productVersion"`
	BOMSHA256      string                  `json:"bomSha256"`
	PublishedAt    string                  `json:"publishedAt,omitempty"`
	Components     []DistributionComponent `json:"components,omitempty"`
}

// UpdateClient talks to the update service admin and client APIs.
type UpdateClient struct {
	BaseURL string
	Creds   Provider
	HTTP    *http.Client
}

func (c *UpdateClient) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Minute}
}

func (c *UpdateClient) url(path string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return "", fmt.Errorf("%w: update service URL is not configured", ErrInvalid)
	}
	return base + path, nil
}

// EnsureComponent registers (or returns) a component definition.
func (c *UpdateClient) EnsureComponent(ctx context.Context, credentialRef string, in EnsureComponentRequest) (*Component, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, fmt.Errorf("%w: component name is required", ErrInvalid)
	}
	payload := map[string]string{"name": strings.TrimSpace(in.Name)}
	if v := strings.TrimSpace(in.DisplayName); v != "" {
		payload["displayName"] = v
	}
	if v := strings.TrimSpace(in.Kind); v != "" {
		payload["kind"] = v
	}
	if v := strings.TrimSpace(in.Versioning); v != "" {
		payload["versioning"] = v
	}
	if v := strings.TrimSpace(in.MinProductVersion); v != "" {
		payload["minProductVersion"] = v
	}
	var out Component
	if err := c.do(ctx, credentialRef, http.MethodPost, "/v1/admin/components", payload, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListReleases lists releases of one component.
func (c *UpdateClient) ListReleases(ctx context.Context, credentialRef, component string) ([]Release, error) {
	path := "/v1/admin/releases"
	if component = strings.TrimSpace(component); component != "" {
		path += "?component=" + url.QueryEscape(component)
	}
	var out struct {
		Releases []Release `json:"releases"`
	}
	if err := c.do(ctx, credentialRef, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Releases, nil
}

// FindRelease returns the release for (component, channel, version) or
// ErrNotFoundRemote.
func (c *UpdateClient) FindRelease(ctx context.Context, credentialRef, component, channel, version string) (*Release, error) {
	releases, err := c.ListReleases(ctx, credentialRef, component)
	if err != nil {
		return nil, err
	}
	for i := range releases {
		r := releases[i]
		if r.Channel == channel && r.Version == version {
			return &r, nil
		}
	}
	return nil, fmt.Errorf("%w: release %s %s/%s", ErrNotFoundRemote, component, channel, version)
}

// CreateRelease creates an unpublished release.
func (c *UpdateClient) CreateRelease(ctx context.Context, credentialRef string, in CreateReleaseRequest) (*Release, error) {
	if strings.TrimSpace(in.Component) == "" || strings.TrimSpace(in.Version) == "" || strings.TrimSpace(in.Channel) == "" {
		return nil, fmt.Errorf("%w: component, version and channel are required", ErrInvalid)
	}
	payload := map[string]any{
		"component": strings.TrimSpace(in.Component),
		"version":   strings.TrimSpace(in.Version),
		"channel":   strings.TrimSpace(in.Channel),
		"publish":   false,
	}
	if v := strings.TrimSpace(in.Notes); v != "" {
		payload["notes"] = v
	}
	if v := strings.TrimSpace(in.NotesURL); v != "" {
		payload["notesUrl"] = v
	}
	if v := strings.TrimSpace(in.MinimumSupportedVersion); v != "" {
		payload["minimumSupportedVersion"] = v
	}
	var out Release
	if err := c.do(ctx, credentialRef, http.MethodPost, "/v1/admin/releases", payload, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetRelease reads a release and its indexed artifacts.
func (c *UpdateClient) GetRelease(ctx context.Context, credentialRef, releaseID string) (*ReleaseDetail, error) {
	releaseID = strings.TrimSpace(releaseID)
	if releaseID == "" {
		return nil, fmt.Errorf("%w: release id is required", ErrInvalid)
	}
	var out ReleaseDetail
	if err := c.do(ctx, credentialRef, http.MethodGet, "/v1/admin/releases/"+url.PathEscape(releaseID), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UploadArtifact streams artifact bytes into one (slot, os, arch) cell. The
// service hashes the body itself; sha256 is an assertion, not the source of
// truth. Content artifacts pass empty os/arch and are addressed with the "any"
// sentinel segment.
func (c *UpdateClient) UploadArtifact(ctx context.Context, credentialRef, releaseID, slot, platformOS, arch, filename, expectedSHA256 string, size int64, body io.Reader) (*Artifact, error) {
	releaseID = strings.TrimSpace(releaseID)
	slot = strings.TrimSpace(slot)
	if releaseID == "" || slot == "" {
		return nil, fmt.Errorf("%w: release id and slot are required", ErrInvalid)
	}
	if body == nil {
		return nil, fmt.Errorf("%w: artifact body is required", ErrInvalid)
	}
	path := fmt.Sprintf("/v1/admin/releases/%s/artifacts/%s/%s/%s",
		url.PathEscape(releaseID), url.PathEscape(slot), platformSegment(platformOS), platformSegment(arch))
	headers := map[string]string{"Content-Type": "application/octet-stream"}
	if v := strings.TrimSpace(filename); v != "" {
		headers["X-Filename"] = v
	}
	if v := strings.TrimSpace(expectedSHA256); v != "" {
		headers["X-Sha256"] = strings.TrimPrefix(v, "sha256:")
	}
	var out Artifact
	if err := c.doStream(ctx, credentialRef, http.MethodPut, path, body, size, headers, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PublishRelease points the (component, channel) pointer at this release.
func (c *UpdateClient) PublishRelease(ctx context.Context, credentialRef, releaseID string) error {
	releaseID = strings.TrimSpace(releaseID)
	if releaseID == "" {
		return fmt.Errorf("%w: release id is required", ErrInvalid)
	}
	return c.do(ctx, credentialRef, http.MethodPost, "/v1/admin/releases/"+url.PathEscape(releaseID)+"/publish", nil, nil, nil)
}

// GetDistribution reads one BOM by channel and product version.
func (c *UpdateClient) GetDistribution(ctx context.Context, credentialRef, channel, productVersion string) (*Distribution, error) {
	path := "/v1/admin/distributions?channel=" + url.QueryEscape(channel)
	var out struct {
		Distributions []Distribution `json:"distributions"`
	}
	if err := c.do(ctx, credentialRef, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	for i := range out.Distributions {
		if out.Distributions[i].ProductVersion == productVersion {
			return &out.Distributions[i], nil
		}
	}
	return nil, fmt.Errorf("%w: distribution %s/%s", ErrNotFoundRemote, channel, productVersion)
}

// PublishDistribution publishes or advances the channel BOM. The service
// resolves each (component, version) to its release id.
func (c *UpdateClient) PublishDistribution(ctx context.Context, credentialRef string, in PublishDistributionRequest) (*Distribution, error) {
	in.Channel = strings.TrimSpace(in.Channel)
	in.ProductVersion = strings.TrimSpace(in.ProductVersion)
	if in.Channel == "" || in.ProductVersion == "" || len(in.Components) == 0 {
		return nil, fmt.Errorf("%w: channel, productVersion and at least one component are required", ErrInvalid)
	}
	components := make([]map[string]string, 0, len(in.Components))
	for _, component := range in.Components {
		components = append(components, map[string]string{
			"name":    strings.TrimSpace(component.Name),
			"version": strings.TrimSpace(component.Version),
		})
	}
	payload := map[string]any{
		"channel":        in.Channel,
		"productVersion": in.ProductVersion,
		"components":     components,
	}
	var out Distribution
	if err := c.do(ctx, credentialRef, http.MethodPost, "/v1/admin/distributions", payload, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Latest reads the signed distribution of a channel and returns the pointer a
// client would follow. The service signs an envelope; only the payload is
// needed here.
func (c *UpdateClient) Latest(ctx context.Context, credentialRef, channel string) (*ChannelPointer, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		channel = "stable"
	}
	var envelope struct {
		Payload   json.RawMessage `json:"payload"`
		Signature string          `json:"signature"`
	}
	if err := c.do(ctx, credentialRef, http.MethodGet, "/v1/distributions/latest?channel="+url.QueryEscape(channel), nil, nil, &envelope); err != nil {
		return nil, err
	}
	payload := struct {
		Channel        string                  `json:"channel"`
		ProductVersion string                  `json:"productVersion"`
		PublishedAt    string                  `json:"publishedAt"`
		Components     []DistributionComponent `json:"components"`
	}{}
	if len(envelope.Payload) == 0 {
		return nil, fmt.Errorf("%w: latest distribution is not a signed envelope", ErrRemote)
	}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return nil, fmt.Errorf("%w: decode distribution payload: %v", ErrRemote, err)
	}
	pointer := &ChannelPointer{
		Channel:        payload.Channel,
		ProductVersion: payload.ProductVersion,
		PublishedAt:    payload.PublishedAt,
		Components:     payload.Components,
	}
	pointer.BOMSHA256 = DistributionDigest(pointer.Channel, pointer.ProductVersion, pointer.Components)
	return pointer, nil
}

// DistributionDigest is a stable digest of what a channel serves: product
// version plus the sorted component versions. It deliberately ignores the
// signature expiry so two reads of the same BOM compare equal.
func DistributionDigest(channel, productVersion string, components []DistributionComponent) string {
	ordered := append([]DistributionComponent(nil), components...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	var buf strings.Builder
	buf.WriteString(strings.TrimSpace(channel))
	buf.WriteString("\n")
	buf.WriteString(strings.TrimSpace(productVersion))
	for _, component := range ordered {
		buf.WriteString("\n")
		buf.WriteString(strings.TrimSpace(component.Name))
		buf.WriteString("=")
		buf.WriteString(strings.TrimSpace(component.Version))
	}
	sum := sha256.Sum256([]byte(buf.String()))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// platformSegment mirrors the server convention: empty os/arch uses "any".
func platformSegment(value string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" && !strings.EqualFold(trimmed, "any") {
		return url.PathEscape(trimmed)
	}
	return "any"
}

func (c *UpdateClient) do(ctx context.Context, credentialRef, method, path string, payload any, headers map[string]string, out any) error {
	var body io.Reader
	var size int64
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("%w: encode request: %v", ErrInvalid, err)
		}
		body = bytes.NewReader(raw)
		size = int64(len(raw))
		if headers == nil {
			headers = map[string]string{}
		}
		if _, ok := headers["Content-Type"]; !ok {
			headers["Content-Type"] = "application/json"
		}
	}
	return c.doStream(ctx, credentialRef, method, path, body, size, headers, out)
}

func (c *UpdateClient) doStream(ctx context.Context, credentialRef, method, path string, body io.Reader, size int64, headers map[string]string, out any) error {
	target, err := c.url(path)
	if err != nil {
		return err
	}
	secret := ""
	if strings.TrimSpace(credentialRef) != "" {
		if c.Creds == nil {
			return fmt.Errorf("%w: no credential provider", ErrCredentialMissing)
		}
		secret, err = c.Creds.Resolve(ctx, credentialRef)
		if err != nil {
			return err
		}
	}
	var reader io.Reader
	if body != nil {
		// The transport closes the request body; wrap it so the caller keeps
		// ownership of the file or object it opened.
		reader = noCloseReader{reader: body}
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return fmt.Errorf("%w: build request: %v", ErrInvalid, err)
	}
	if body != nil && size > 0 {
		req.ContentLength = size
	}
	req.Header.Set("Accept", "application/json")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRemote, err)
	}
	defer resp.Body.Close()
	content, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s", ErrNotFoundRemote, remoteMessage(content, resp.Status))
	}
	if resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusPreconditionFailed {
		return fmt.Errorf("%w: %s", ErrConflictRemote, remoteMessage(content, resp.Status))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: %s", ErrRemote, remoteMessage(content, resp.Status))
	}
	if readErr != nil {
		return fmt.Errorf("%w: read response: %v", ErrRemote, readErr)
	}
	if out == nil || len(bytes.TrimSpace(content)) == 0 {
		return nil
	}
	if err := json.Unmarshal(content, out); err != nil {
		return fmt.Errorf("%w: decode response: %v", ErrRemote, err)
	}
	return nil
}

// noCloseReader hides Close from net/http so the caller decides when the
// underlying object is released.
type noCloseReader struct{ reader io.Reader }

func (r noCloseReader) Read(p []byte) (int, error) { return r.reader.Read(p) }

func remoteMessage(payload []byte, status string) string {
	msg := strings.TrimSpace(string(payload))
	var parsed struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(payload, &parsed) == nil {
		if parsed.Error != "" && parsed.Message != "" {
			msg = parsed.Error + ": " + parsed.Message
		} else if parsed.Message != "" {
			msg = parsed.Message
		} else if parsed.Error != "" {
			msg = parsed.Error
		}
	}
	if len(msg) > 400 {
		msg = msg[:400]
	}
	if msg == "" {
		return status
	}
	return status + ": " + msg
}
