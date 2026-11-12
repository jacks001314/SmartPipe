package releasemcp

import (
	"strings"
	"testing"

	"smartpipe/backend/internal/releasepublish"
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
