package releasev2

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func bundleFixture() (*Run, []Target, []Artifact, BundleLayout) {
	run := &Run{
		ID: "rrun_1", Version: "0.5.16", BuildVersion: "0.5.16.2026-11-12", Channel: "stable",
		ResolvedCommit: "4e5ed363519a8e9f9edebf7f800f48b5c9837fec", ProfileSHA256: "sha256:abc", Status: RunStaged,
	}
	targets := []Target{
		{TargetID: "darwin-amd64", OS: "darwin", Arch: "amd64", Required: true, Status: TargetReady},
		{TargetID: "windows-amd64", OS: "windows", Arch: "amd64", Required: true, Status: TargetReady},
	}
	digestA := "sha256:" + strings.Repeat("a", 64)
	digestB := "sha256:" + strings.Repeat("b", 64)
	artifacts := []Artifact{
		{ID: 1, RunID: run.ID, TargetID: "windows-amd64", Role: "desktop-installer", Name: "SyntropySetup-0.5.16.exe", Size: 1024, SHA256: digestA, ContentType: "application/x-msdownload"},
		{ID: 2, RunID: run.ID, TargetID: "darwin-amd64", Role: "desktop-installer", Name: "Syntropy-0.5.16.dmg", Size: 2048, SHA256: digestB, ContentType: "application/x-apple-diskimage"},
	}
	layout := BundleLayout{
		Product: "product",
		Slots:   map[string]string{"desktop": "desktop-installer"},
		Sources: BundleSources{Primary: "https://app.example.com", Mirrors: []string{"https://cdn2.example.com", "https://cdn1.example.com"}},
	}
	return run, targets, artifacts, layout
}

func TestBuildUpgradeBundleIsCanonical(t *testing.T) {
	run, targets, artifacts, layout := bundleFixture()
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	bundle, raw, digest, err := BuildUpgradeBundle(run, targets, artifacts, layout, now)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.ProductVersion != "0.5.16" || bundle.BuildVersion != "0.5.16.2026-11-12" {
		t.Fatalf("versions = %s/%s", bundle.ProductVersion, bundle.BuildVersion)
	}
	slots := bundle.Components["product"].Slots
	if len(slots) != 2 || slots[0].OS != "darwin" || slots[1].OS != "windows" {
		t.Fatalf("slots = %+v", slots)
	}
	if bundle.Sources.Mirrors[0] != "https://cdn1.example.com" {
		t.Fatalf("mirrors not sorted: %+v", bundle.Sources.Mirrors)
	}
	if !strings.Contains(string(raw), `"schemaVersion":1`) {
		t.Fatalf("raw bundle = %s", raw)
	}
	_, raw2, digest2, err := BuildUpgradeBundle(run, targets, artifacts, layout, now)
	if err != nil {
		t.Fatal(err)
	}
	if digest != digest2 || string(raw) != string(raw2) {
		t.Fatalf("bundle is not deterministic")
	}

	artifacts[0].SHA256 = "sha256:" + strings.Repeat("c", 64)
	_, _, digest3, err := BuildUpgradeBundle(run, targets, artifacts, layout, now)
	if err != nil {
		t.Fatal(err)
	}
	if digest3 == digest {
		t.Fatal("artifact change did not change the bundle digest")
	}
}

func TestBuildUpgradeBundleRejectsMissingAndDuplicateSlots(t *testing.T) {
	run, targets, artifacts, layout := bundleFixture()
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	if _, _, _, err := BuildUpgradeBundle(run, targets, artifacts[:1], layout, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing slot err = %v", err)
	}
	dup := append(append([]Artifact{}, artifacts...), artifacts[0])
	dup[2].Name = "SyntropySetup-alt.exe"
	if _, _, _, err := BuildUpgradeBundle(run, targets, dup, layout, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate slot err = %v", err)
	}
}

func TestBuildUpgradeBundleRejectsBadDigest(t *testing.T) {
	run, targets, artifacts, layout := bundleFixture()
	artifacts[1].SHA256 = "sha256:nothex"
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	if _, _, _, err := BuildUpgradeBundle(run, targets, artifacts, layout, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad digest err = %v", err)
	}
}

func TestCurrentAttemptArtifactsDropsStaleAttempts(t *testing.T) {
	run, targets, artifacts, _ := bundleFixture()
	targets[0].Attempt = 2
	targets[1].Attempt = 1
	artifacts[0].Attempt = 1
	artifacts[1].Attempt = 2
	stale := artifacts[1]
	stale.ID = 3
	stale.Attempt = 1
	artifacts = append(artifacts, stale)
	runLevel := Artifact{ID: 4, RunID: run.ID, Role: "upgrade_bundle", Name: "upgrade-bundle.json"}
	artifacts = append(artifacts, runLevel)

	got := CurrentAttemptArtifacts(targets, artifacts)
	if len(got) != 3 {
		t.Fatalf("kept %d artifacts, want 3: %+v", len(got), got)
	}
	for _, a := range got {
		if a.ID == 3 {
			t.Fatal("stale darwin attempt 1 artifact was kept")
		}
	}
	if got[2].ID != 4 {
		t.Fatalf("run-level artifact was dropped: %+v", got)
	}
}

func TestBuildUpgradeBundleIsStableAcrossRebuilds(t *testing.T) {
	run, targets, artifacts, layout := bundleFixture()
	run.CreatedAt = time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)
	targets[0].Attempt = 1
	targets[1].Attempt = 1
	first := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	second := time.Date(2026, 11, 12, 11, 30, 0, 0, time.UTC)
	_, rawA, digestA, err := BuildUpgradeBundle(run, targets, artifacts, layout, first)
	if err != nil {
		t.Fatal(err)
	}
	_, rawB, digestB, err := BuildUpgradeBundle(run, targets, artifacts, layout, second)
	if err != nil {
		t.Fatal(err)
	}
	if digestA != digestB || string(rawA) != string(rawB) {
		t.Fatalf("rebuild changed the bundle digest: %s vs %s", digestA, digestB)
	}
}

// 一台构建机可以交叉产出其它平台的组件：槽位平台必须取自制品本身。
func TestBuildUpgradeBundleUsesArtifactPlatformForCrossBuilds(t *testing.T) {
	run, _, _, layout := bundleFixture()
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	targets := []Target{{TargetID: "mac-01", OS: "darwin", Arch: "amd64", Required: true, Status: TargetReady}}
	artifacts := []Artifact{
		{ID: 1, RunID: run.ID, TargetID: "mac-01", Role: "desktop-installer", Name: "Syntropy-0.5.16.dmg", Size: 2048, SHA256: "sha256:" + strings.Repeat("d", 64)},
		{ID: 2, RunID: run.ID, TargetID: "mac-01", Role: "desktop-installer", OS: "windows", Arch: "AMD64", Name: "SyntropySetup-0.5.16.exe", Size: 4096, SHA256: "sha256:" + strings.Repeat("e", 64)},
	}
	bundle, _, _, err := BuildUpgradeBundle(run, targets, artifacts, layout, now)
	if err != nil {
		t.Fatal(err)
	}
	slots := bundle.Components["product"].Slots
	if len(slots) != 2 {
		t.Fatalf("slots = %+v", slots)
	}
	if slots[0].OS != "darwin" || slots[0].Arch != "amd64" || slots[1].OS != "windows" || slots[1].Arch != "amd64" {
		t.Fatalf("cross-build slots = %+v", slots)
	}
	if slots[1].Name != "SyntropySetup-0.5.16.exe" {
		t.Fatalf("windows slot = %+v", slots[1])
	}

	// 必需的 target 如果一条制品都没贡献，仍然要报错。
	extra := append(append([]Target{}, targets...), Target{TargetID: "windows-01", OS: "windows", Arch: "amd64", Required: true, Status: TargetReady})
	if _, _, _, err := BuildUpgradeBundle(run, extra, artifacts, layout, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("uncovered required target err = %v", err)
	}
}

func TestBuildUpgradeBundleDerivesContentVersionFromArtifactName(t *testing.T) {
	run, targets, artifacts, layout := bundleFixture()
	layout.Components = map[string]string{"content-templates": "templates"}
	layout.ComponentVersions = map[string]string{}
	artifacts = append(artifacts, Artifact{
		ID: 9, RunID: run.ID, TargetID: "darwin-amd64", Role: "content-templates",
		Name: "templates-1.2.0.tar.gz", Size: 512, SHA256: "sha256:" + strings.Repeat("f", 64),
	})
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	bundle, _, _, err := BuildUpgradeBundle(run, targets, artifacts, layout, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := bundle.Components["templates"].Version; got != "1.2.0" {
		t.Fatalf("templates version = %q", got)
	}

	artifacts[len(artifacts)-1].Name = "templates-latest.tar.gz"
	if _, _, _, err := BuildUpgradeBundle(run, targets, artifacts, layout, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unversioned content artifact err = %v", err)
	}
}
