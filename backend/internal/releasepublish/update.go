package releasepublish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// ImportRequest is the payload sent to POST /v1/admin/update-imports.
type ImportRequest struct {
	Product        string          `json:"product"`
	Channel        string          `json:"channel"`
	ProductVersion string          `json:"productVersion"`
	BuildVersion   string          `json:"buildVersion"`
	BundleSHA256   string          `json:"bundleSha256"`
	BundleURL      string          `json:"bundleUrl,omitempty"`
	BundleJSON     json.RawMessage `json:"bundle,omitempty"`
	IdempotencyKey string          `json:"idempotencyKey"`
}

// Import is the upgrade service staging record.
type Import struct {
	ID             string         `json:"id"`
	State          string         `json:"state"`
	Channel        string         `json:"channel"`
	ProductVersion string         `json:"productVersion"`
	BuildVersion   string         `json:"buildVersion"`
	BOMSHA256      string         `json:"bomSha256"`
	Slots          []ImportSlot   `json:"slots,omitempty"`
	Error          string         `json:"error,omitempty"`
	Extra          map[string]any `json:"-"`
}

type ImportSlot struct {
	Slot   string `json:"slot"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	State  string `json:"state,omitempty"`
}

type PromoteRequest struct {
	ExpectedCurrentProductVersion string `json:"expectedCurrentProductVersion"`
	ExpectedBOMSHA256             string `json:"expectedBomSha256"`
	IdempotencyKey                string `json:"idempotencyKey"`
}

type PromoteResult struct {
	State          string `json:"state"`
	Channel        string `json:"channel"`
	ProductVersion string `json:"productVersion"`
	ReleaseID      string `json:"releaseId"`
	BOMSHA256      string `json:"bomSha256"`
	PromotedAt     string `json:"promotedAt,omitempty"`
}

// UpdateClient talks to the two phase upgrade service admin API.
type UpdateClient struct {
	BaseURL string
	Creds   Provider
	HTTP    *http.Client
}

func (c *UpdateClient) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

func (c *UpdateClient) url(path string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return "", fmt.Errorf("%w: update service URL is not configured", ErrInvalid)
	}
	return base + path, nil
}

func (c *UpdateClient) Stage(ctx context.Context, credentialRef string, in ImportRequest) (*Import, error) {
	if strings.TrimSpace(in.Product) == "" || strings.TrimSpace(in.Channel) == "" || strings.TrimSpace(in.ProductVersion) == "" {
		return nil, fmt.Errorf("%w: product, channel and productVersion are required", ErrInvalid)
	}
	if strings.TrimSpace(in.BundleSHA256) == "" || strings.TrimSpace(in.IdempotencyKey) == "" {
		return nil, fmt.Errorf("%w: bundleSha256 and idempotencyKey are required", ErrInvalid)
	}
	var out Import
	if err := c.do(ctx, credentialRef, http.MethodPost, "/v1/admin/update-imports", in, &out); err != nil {
		return nil, err
	}
	if out.ID == "" {
		return nil, fmt.Errorf("%w: update service returned no import id", ErrRemote)
	}
	return &out, nil
}

func (c *UpdateClient) Get(ctx context.Context, credentialRef, importID string) (*Import, error) {
	importID = strings.TrimSpace(importID)
	if importID == "" {
		return nil, fmt.Errorf("%w: import id is required", ErrInvalid)
	}
	var out Import
	if err := c.do(ctx, credentialRef, http.MethodGet, "/v1/admin/update-imports/"+importID, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *UpdateClient) Promote(ctx context.Context, credentialRef, importID string, in PromoteRequest) (*PromoteResult, error) {
	importID = strings.TrimSpace(importID)
	if importID == "" {
		return nil, fmt.Errorf("%w: import id is required", ErrInvalid)
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		return nil, fmt.Errorf("%w: idempotencyKey is required", ErrInvalid)
	}
	var out PromoteResult
	if err := c.do(ctx, credentialRef, http.MethodPost, "/v1/admin/update-imports/"+importID+"/promote", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ChannelPointer is the currently promoted release for one channel.
type ChannelPointer struct {
	Channel        string `json:"channel"`
	ProductVersion string `json:"productVersion"`
	BuildVersion   string `json:"buildVersion"`
	ReleaseID      string `json:"releaseId"`
	BOMSHA256      string `json:"bomSha256"`
	PromotedAt     string `json:"promotedAt,omitempty"`
}

// Latest reads the channel pointer so callers can preflight or verify a promote.
func (c *UpdateClient) Latest(ctx context.Context, credentialRef, channel string) (*ChannelPointer, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		channel = "stable"
	}
	var out ChannelPointer
	if err := c.do(ctx, credentialRef, http.MethodGet, "/v1/distributions/latest?channel="+url.QueryEscape(channel), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *UpdateClient) Cancel(ctx context.Context, credentialRef, importID string) error {
	importID = strings.TrimSpace(importID)
	if importID == "" {
		return fmt.Errorf("%w: import id is required", ErrInvalid)
	}
	return c.do(ctx, credentialRef, http.MethodPost, "/v1/admin/update-imports/"+importID+"/cancel", nil, nil)
}

func (c *UpdateClient) do(ctx context.Context, credentialRef, method, path string, body any, out any) error {
	url, err := c.url(path)
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
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Idempotency-Key", headerValue(body))
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRemote, err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s", ErrNotFoundRemote, remoteMessage(payload, resp.Status))
	}
	if resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusPreconditionFailed {
		return fmt.Errorf("%w: %s", ErrConflictRemote, remoteMessage(payload, resp.Status))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: %s", ErrRemote, remoteMessage(payload, resp.Status))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		// Some upgrade servers wrap the payload; retry one level deep.
		var wrapper struct {
			Import  *Import         `json:"import"`
			Result  *PromoteResult  `json:"result"`
			RawJSON json.RawMessage `json:"-"`
		}
		if json.Unmarshal(payload, &wrapper) == nil {
			if imp, ok := out.(*Import); ok && wrapper.Import != nil {
				*imp = *wrapper.Import
				return nil
			}
			if pr, ok := out.(*PromoteResult); ok && wrapper.Result != nil {
				*pr = *wrapper.Result
				return nil
			}
		}
		return fmt.Errorf("%w: decode response: %v", ErrRemote, err)
	}
	return nil
}

func headerValue(body any) string {
	if req, ok := body.(ImportRequest); ok {
		return req.IdempotencyKey
	}
	if req, ok := body.(PromoteRequest); ok {
		return req.IdempotencyKey
	}
	return ""
}

func remoteMessage(payload []byte, status string) string {
	msg := strings.TrimSpace(string(payload))
	var parsed struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(payload, &parsed) == nil {
		if parsed.Error != "" {
			msg = parsed.Error
		} else if parsed.Message != "" {
			msg = parsed.Message
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
