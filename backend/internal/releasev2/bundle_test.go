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
