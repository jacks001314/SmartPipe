package testlab

import (
	"strings"
	"testing"
	"time"
)

func fixture() (Spec, Report) {
	s := Spec{ProjectID: "p", SourceSHA: strings.Repeat("a", 40), ArtifactSHA256: strings.Repeat("b", 64), Scenario: "desktop", ScenarioVersion: "1", Platform: "windows-amd64", RequiredChecks: []string{"file", "ui"}}
	r := Report{SourceSHA: s.SourceSHA, ArtifactSHA256: s.ArtifactSHA256, ScenarioVersion: s.ScenarioVersion, Platform: s.Platform, Checks: []Check{{"file", "PASS", "evidence/file.json"}, {"ui", "PASS", "evidence/screen.png"}}}
	return s, r
}
func TestEvaluate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Report)
		want    string
		invalid bool
	}{
		{"pass", func(r *Report) {}, "PASS", false},
		{"missing", func(r *Report) { r.Checks = r.Checks[:1] }, "BLOCKED", false},
		{"skip", func(r *Report) { r.Checks[0].Status = "SKIPPED" }, "BLOCKED", false},
		{"no evidence", func(r *Report) { r.Checks[0].Evidence = "" }, "BLOCKED", false},
		{"fail", func(r *Report) { r.Checks[0].Status = "FAIL" }, "FAIL", false},
		{"wrong artifact", func(r *Report) { r.ArtifactSHA256 = strings.Repeat("c", 64) }, "", true},
		{"wrong platform", func(r *Report) { r.Platform = "darwin-arm64" }, "", true},
		{"duplicate", func(r *Report) { r.Checks = append(r.Checks, r.Checks[0]) }, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, r := fixture()
			c.mutate(&r)
			got, err := Evaluate(s, r)
			if (err != nil) != c.invalid || got != c.want {
				t.Fatalf("got %q %v", got, err)
			}
		})
	}
}
func TestGateUsesCurrentPolicy(t *testing.T) {
	s, r := fixture()
	now := time.Now()
	run := Run{Spec: s, Status: "PASS", Report: &r, FinishedAt: &now}
	if err := MatchesPassedRun(run, s); err != nil {
		t.Fatal(err)
	}
	s.RequiredChecks = append(s.RequiredChecks, "upgrade")
	if err := MatchesPassedRun(run, s); err == nil {
		t.Fatal("old report passed new policy")
	}
}
func TestRejectEmptyPolicy(t *testing.T) {
	s, _ := fixture()
	s.RequiredChecks = nil
	if s.Validate() == nil {
		t.Fatal("empty policy accepted")
	}
}
