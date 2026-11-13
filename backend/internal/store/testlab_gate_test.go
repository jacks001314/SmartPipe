package store

import (
	"context"
	"smartpipe/backend/internal/testlab"
	"strings"
	"testing"
)

func TestObservationCannotEnableReleaseGate(t *testing.T) {
	var s *Store
	err := s.SetDesktopGatePolicies(context.Background(), "p", []DesktopGatePolicy{{
		Scenario: "desktop-observe", Version: "1", Platform: "windows-amd64", ArtifactRole: "installer", RequiredChecks: []string{"desktop_observed"},
	}})
	if err == nil {
		t.Fatal("observation accepted as release gate")
	}
}
func TestDuplicateGateChecksRejected(t *testing.T) {
	var s *Store
	err := s.SetDesktopGatePolicies(context.Background(), "p", []DesktopGatePolicy{{
		Scenario: "installed-code-fix", Version: "1", Platform: "windows-amd64", ArtifactRole: "installer", RequiredChecks: []string{"ui", "ui"},
	}})
	if err == nil {
		t.Fatal("duplicate checks accepted")
	}
}

func TestReleaseArtifactDigestMatchesDesktopSpec(t *testing.T) {
	digest := strings.Repeat("b", 64)
	for _, input := range []string{digest, "sha256:" + digest} {
		spec := testlab.Spec{ProjectID: "p", SourceSHA: strings.Repeat("a", 40), ArtifactSHA256: desktopArtifactDigest(input), Scenario: "installed", ScenarioVersion: "1", Platform: "windows-amd64", RequiredChecks: []string{"ui"}}
		if err := spec.Validate(); err != nil {
			t.Fatal(err)
		}
		if spec.ArtifactSHA256 != digest {
			t.Fatalf("digest mismatch: %s", spec.ArtifactSHA256)
		}
	}
	if desktopArtifactDigest("sha512:"+digest) == digest {
		t.Fatal("accepted wrong algorithm")
	}
}
