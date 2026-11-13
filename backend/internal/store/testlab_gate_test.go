package store

import (
	"context"
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
