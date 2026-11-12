package releasemcp

import (
	"strings"
	"testing"

	"smartpipe/backend/internal/releasepublish"
	"smartpipe/backend/internal/releasev2"
)

func TestRepoSlugParsesCommonURLs(t *testing.T) {
	cases := []struct{ in, owner, repo string }{
		{"https://github.com/acme/smart-pipe.git", "acme", "smart-pipe"},
		{"https://github.com/acme/smart-pipe", "acme", "smart-pipe"},
		{"git@github.com:acme/smart-pipe.git", "acme", "smart-pipe"},
		{"acme/smart-pipe", "acme", "smart-pipe"},
	}
	for _, tc := range cases {
		owner, repo, err := repoSlug(tc.in)
		if err != nil || owner != tc.owner || repo != tc.repo {
			t.Fatalf("repoSlug(%q) = %q/%q err=%v", tc.in, owner, repo, err)
		}
	}
	if _, _, err := repoSlug(""); err == nil {
		t.Fatal("empty url accepted")
	}
}

func TestVerifyGitHubAssetsDetectsGaps(t *testing.T) {
	digest := "sha256:" + "a"
	artifacts := []releasev2.Artifact{
		{Name: "SyntropySetup.exe", Role: "server-binary", SHA256: digest},
		{Name: "upgrade-bundle.json", Role: "upgrade_bundle", SHA256: digest},
	}
	release := &releasepublish.GitHubRelease{Assets: []releasepublish.GitHubAsset{{Name: "SyntropySetup.exe", Digest: digest}}}
	if gaps := verifyGitHubAssets(release, artifacts); len(gaps) != 0 {
		t.Fatalf("gaps = %v", gaps)
	}
	release.Assets[0].Digest = "sha256:other"
	if gaps := verifyGitHubAssets(release, artifacts); len(gaps) != 1 {
		t.Fatalf("digest mismatch gaps = %v", gaps)
	}
	release.Assets = nil
	if gaps := verifyGitHubAssets(release, artifacts); len(gaps) != 1 {
		t.Fatalf("missing asset gaps = %v", gaps)
	}
}

func TestVerifyGitHubAssets(t *testing.T) {
	artifacts := []releasev2.Artifact{
		{Name: "app.tar.gz", Role: "app-bundle", Size: 176, SHA256: "sha256:aaaa"},
		{Name: "upgrade-bundle.json", Role: "upgrade_bundle", Size: 722, SHA256: "sha256:bbbb"},
	}
	release := &releasepublish.GitHubRelease{Assets: []releasepublish.GitHubAsset{
		{Name: "app.tar.gz", Size: 176, Digest: "aaaa"},
	}}
	if gaps := verifyGitHubAssets(release, artifacts); len(gaps) != 0 {
		t.Fatalf("prefixed/bare digest should match: %v", gaps)
	}
	release.Assets[0].Digest = "sha256:cccc"
	if gaps := verifyGitHubAssets(release, artifacts); len(gaps) != 1 || !strings.Contains(gaps[0], "digest mismatch") {
		t.Fatalf("digest mismatch not reported: %v", gaps)
	}
	release.Assets[0].Digest = ""
	if gaps := verifyGitHubAssets(release, artifacts); len(gaps) != 0 {
		t.Fatalf("size fallback should pass: %v", gaps)
	}
	release.Assets[0].Size = 1
	if gaps := verifyGitHubAssets(release, artifacts); len(gaps) != 1 || !strings.Contains(gaps[0], "size mismatch") {
		t.Fatalf("size mismatch not reported: %v", gaps)
	}
	if gaps := verifyGitHubAssets(&releasepublish.GitHubRelease{}, artifacts); len(gaps) != 1 || !strings.Contains(gaps[0], "missing asset app.tar.gz") {
		t.Fatalf("missing asset not reported: %v", gaps)
	}
}

func TestPickArtifact(t *testing.T) {
	artifacts := []releasev2.Artifact{
		{ID: 1, Name: "app.tar.gz", Role: "app-bundle"},
		{ID: 2, Name: "app.zip", Role: "app-bundle"},
	}
	if got, err := pickArtifact(artifacts, 2, "", ""); err != nil || got.Name != "app.zip" {
		t.Fatalf("by id: %+v %v", got, err)
	}
	if got, err := pickArtifact(artifacts, 0, "app.tar.gz", ""); err != nil || got.ID != 1 {
		t.Fatalf("by name: %+v %v", got, err)
	}
	if _, err := pickArtifact(artifacts, 0, "", "app-bundle"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous role should be rejected: %v", err)
	}
	if _, err := pickArtifact(artifacts, 0, "", ""); err == nil {
		t.Fatal("empty selector should be rejected")
	}
}
