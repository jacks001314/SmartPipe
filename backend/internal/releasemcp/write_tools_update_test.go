package releasemcp

import (
	"strings"
	"testing"

	"smartpipe/backend/internal/releasepublish"
	"smartpipe/backend/internal/releasev2"
)

func TestContentArtifactNameUsesServiceConvention(t *testing.T) {
	cases := []struct {
		name     string
		filename string
		want     string
		wantErr  bool
	}{
		{"templates", "templates-0.5.16.tar.gz", "templates-0.5.16.tar.gz", false},
		{"templates", "templates-bundle.tar.gz", "templates-0.5.16.tar.gz", false},
		{"skills", "skills-0.5.16.zip", "", true},
	}
	for _, tc := range cases {
		got, err := contentArtifactName(tc.name, "0.5.16", tc.filename)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: expected an error for %q", tc.name, tc.filename)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("%s %q = %q, %v; want %q", tc.name, tc.filename, got, err, tc.want)
		}
	}
}

func TestCompareDistributionNamesTheMismatch(t *testing.T) {
	served := []releasepublish.DistributionComponent{{Name: "product", Version: "0.5.16"}, {Name: "skills", Version: "1.0.0"}}
	expected := []releasepublish.DistributionComponent{{Name: "product", Version: "0.5.17"}, {Name: "skills", Version: "1.0.0"}}
	if err := compareDistribution(served, expected); err == nil || !strings.Contains(err.Error(), "product=0.5.17") {
		t.Fatalf("err = %v", err)
	}
	if !sameDistribution(served, []releasepublish.DistributionComponent{{Name: "skills", Version: "1.0.0"}, {Name: "product", Version: "0.5.16"}}) {
		t.Fatal("sameDistribution must ignore component order")
	}
	if sameDistribution(served, expected) {
		t.Fatal("sameDistribution must notice a version change")
	}
}

func TestArtifactCellAndVersionShape(t *testing.T) {
	if got := artifactCell("templates", "", ""); got != "templates/content" {
		t.Fatalf("content cell = %q", got)
	}
	if got := artifactCell("desktop", "windows", "amd64"); got != "desktop/windows/amd64" {
		t.Fatalf("platform cell = %q", got)
	}
	for _, ok := range []string{"0.5.16", "1.0.0", "10.20.30"} {
		if !isTripleVersion(ok) {
			t.Fatalf("%q should be accepted", ok)
		}
	}
	for _, bad := range []string{"0.5", "0.5.16.2026-11-12", "v1.0.0", ""} {
		if isTripleVersion(bad) {
			t.Fatalf("%q should be rejected", bad)
		}
	}
}

func TestSelectUpdateTargetsHonoursRunSelection(t *testing.T) {
	all := []updateTarget{{Name: "cdn", URL: "https://cdn.example.com"}, {Name: "app", URL: "https://app.example.com"}}
	got, missing := selectUpdateTargets(all, []string{" CDN ", "cdn"})
	if len(missing) != 0 || len(got) != 1 || got[0].Name != "cdn" {
		t.Fatalf("selection = %+v missing = %v", got, missing)
	}
	if got, missing := selectUpdateTargets(all, nil); len(missing) != 0 || len(got) != 2 {
		t.Fatalf("empty selection must keep every service, got %+v", got)
	}
	got, missing = selectUpdateTargets(all, []string{"cdn", "missing"})
	if len(got) != 1 || got[0].Name != "cdn" || len(missing) != 1 || missing[0] != "missing" {
		t.Fatalf("partial selection = %+v missing = %v", got, missing)
	}
}

func TestBundleCoversArtifactsDetectsStaleBundle(t *testing.T) {
	bundle := releasev2.UpgradeBundle{Components: map[string]releasev2.BundleComponent{
		"product": {Version: "0.5.17", Slots: []releasev2.BundleSlot{{Slot: "installer", ArtifactID: 11}}},
		"templates": {Version: "1.0.0", Artifacts: []releasev2.BundleArtifact{{ArtifactID: 12}}},
	}}
	artifacts := []releasev2.Artifact{
		{ID: 11, Role: "installer"},
		{ID: 12, Role: "templates"},
		{ID: 99, Role: "upgrade_bundle"},
	}
	if !bundleCoversArtifacts(bundle, artifacts) {
		t.Fatal("bundle should cover every publishable artifact")
	}
	artifacts = append(artifacts, releasev2.Artifact{ID: 13, Role: "update-thin"})
	if bundleCoversArtifacts(bundle, artifacts) {
		t.Fatal("bundle missing artifact 13 must be treated as stale")
	}
	if bundleCoversArtifacts(releasev2.UpgradeBundle{}, artifacts) {
		t.Fatal("empty bundle is never usable")
	}
}
