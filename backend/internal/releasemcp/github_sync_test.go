package releasemcp

import (
	"testing"

	"smartpipe/backend/internal/releasepublish"
	"smartpipe/backend/internal/releasev2"
)

func TestGitHubStaleAssetsKeepsOtherVersionsAndExpectedFiles(t *testing.T) {
	release := &releasepublish.GitHubRelease{Assets: []releasepublish.GitHubAsset{
		{ID: 1, Name: "Syntropy-arm64-0.5.17.2026-11-14.dmg"},
		{ID: 2, Name: "Syntropy-arm64-0.5.17.2026-11-13.dmg"},
		{ID: 3, Name: "SHA256SUMS"},
		{ID: 4, Name: "Syntropy-arm64-0.5.16.2026-11-12.dmg"},
	}}
	artifacts := []releasev2.Artifact{
		{Name: "Syntropy-arm64-0.5.17.2026-11-13.dmg", Destinations: []string{"github"}},
		{Name: "update-bundle.json", Destinations: []string{"update:full"}},
	}
	stale := githubStaleAssets(release, artifacts, "0.5.17")
	if len(stale) != 1 || stale[0].ID != 1 {
		t.Fatalf("stale = %+v, want only the 2026-11-14 asset", stale)
	}
}

func TestGitHubStaleAssetsEmptyWithoutVersion(t *testing.T) {
	release := &releasepublish.GitHubRelease{Assets: []releasepublish.GitHubAsset{{ID: 1, Name: "x-0.5.17.dmg"}}}
	if got := githubStaleAssets(release, nil, ""); len(got) != 0 {
		t.Fatalf("stale = %+v, want none without version", got)
	}
}
