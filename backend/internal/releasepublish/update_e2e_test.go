package releasepublish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestUpdateServiceEndToEnd runs the adapter against a real syntropy_update
// server. It touches only the alpha channel and is skipped unless the operator
// points it at a disposable instance:
//
//	SMARTPIPE_UPDATE_E2E_URL=http://127.0.0.1:19443 \
//	SMARTPIPE_UPDATE_E2E_TOKEN=... \
//	go test ./internal/releasepublish -run EndToEnd -count=1
func TestUpdateServiceEndToEnd(t *testing.T) {
	base := strings.TrimSpace(os.Getenv("SMARTPIPE_UPDATE_E2E_URL"))
	if base == "" {
		t.Skip("SMARTPIPE_UPDATE_E2E_URL is not set")
	}
	token := strings.TrimSpace(os.Getenv("SMARTPIPE_UPDATE_E2E_TOKEN"))
	if token == "" {
		t.Fatal("SMARTPIPE_UPDATE_E2E_TOKEN is required")
	}
	ctx := context.Background()
	client := &UpdateClient{BaseURL: base, Creds: staticProvider(token)}
	version := fmt.Sprintf("9.9.%d", time.Now().Unix()%1000)
	payload := []byte("smartpipe adapter end-to-end " + version)

	if _, err := client.EnsureComponent(ctx, "e2e", EnsureComponentRequest{
		Name: "product", DisplayName: "Product", Kind: "product", Versioning: "product",
	}); err != nil {
		t.Fatalf("ensure component: %v", err)
	}
	release, err := client.CreateRelease(ctx, "e2e", CreateReleaseRequest{Component: "product", Version: version, Channel: "alpha"})
	if err != nil {
		t.Fatalf("create release: %v", err)
	}
	digest := sha256Hex(payload)
	if _, err := client.UploadArtifact(ctx, "e2e", release.ID, "mbot", "linux", "amd64",
		"mbot-linux-amd64", digest, int64(len(payload)), strings.NewReader(string(payload))); err != nil {
		t.Fatalf("upload artifact: %v", err)
	}
	detail, err := client.GetRelease(ctx, "e2e", release.ID)
	if err != nil || len(detail.Artifacts) != 1 || trimDigest(detail.Artifacts[0].SHA256) != digest {
		t.Fatalf("release detail = %+v, %v", detail, err)
	}
	if err := client.PublishRelease(ctx, "e2e", release.ID); err != nil {
		t.Fatalf("publish release: %v", err)
	}
	bom := []DistributionComponent{{Name: "product", Version: version}}
	if _, err := client.PublishDistribution(ctx, "e2e", PublishDistributionRequest{
		Channel: "alpha", ProductVersion: version, Components: bom,
	}); err != nil {
		t.Fatalf("publish distribution: %v", err)
	}
	pointer, err := client.Latest(ctx, "", "alpha")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if pointer.ProductVersion != version {
		t.Fatalf("channel alpha serves %s, want %s", pointer.ProductVersion, version)
	}
	if want := DistributionDigest("alpha", version, bom); pointer.BOMSHA256 != want {
		t.Fatalf("digest = %s, want %s", pointer.BOMSHA256, want)
	}
	t.Logf("staged and published %s via %s", version, base)
}

type staticProvider string

func (p staticProvider) Resolve(context.Context, string) (string, error) { return string(p), nil }

func sha256Hex(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func trimDigest(value string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "sha256:")
}
