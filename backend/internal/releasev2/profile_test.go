package releasev2

import (
	"errors"
	"testing"
)

func TestParseProfileAndBuildTargets(t *testing.T) {
	raw := []byte(`
version: 1
release:
  source:
    branch: main
  versioning:
    scheme: semver
    default_bump: patch
  targets:
    windows-amd64:
      executor: windows-01
      build: echo build windows
      verify: echo verify windows
      artifacts:
        - role: desktop-installer
          glob: dist/*.exe
    darwin-arm64:
      executor: mac-01
      build: echo build mac
      verify: echo verify mac
      artifacts:
        - role: desktop-installer
          glob: dist/*.dmg
`)
	p, err := ParseProfile(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.TargetIDs(); len(got) != 2 || got[0] != "darwin-arm64" || got[1] != "windows-amd64" {
		t.Fatalf("target ids = %v", got)
	}
	targets, err := p.PlanTargets(map[string]string{"darwin-arm64": "mac-intel-02"}, []string{"darwin-arm64"})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].ExecutorID != "mac-intel-02" || targets[0].BuildCommand == "" {
		t.Fatalf("targets = %+v", targets)
	}
	if len(targets[0].Artifacts) != 1 || targets[0].Artifacts[0].Role != "desktop-installer" || targets[0].Artifacts[0].Glob != "dist/*.dmg" {
		t.Fatalf("artifact rules not carried into target: %+v", targets[0].Artifacts)
	}
	if !targets[0].Artifacts[0].Required {
		t.Fatal("artifact rule should be required by default")
	}
	if _, err := p.ProfileSHA256(); err != nil {
		t.Fatal(err)
	}
}

func TestParseProfileRequiresReleaseSection(t *testing.T) {
	_, err := ParseProfile([]byte("version: 1\ncommands:\n  unit: go test ./...\n"))
	if !errors.Is(err, ErrNoReleaseProfile) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseProfileRejectsBadTarget(t *testing.T) {
	_, err := ParseProfile([]byte(`
release:
  targets:
    windows-amd64:
      build: echo build
      verify: echo verify
      artifacts: []
`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ParseProfile([]byte(`
release:
  targets:
    darwin-arm64:
      build: ""
      verify: echo verify
`))
	if err == nil {
		t.Fatal("empty build accepted")
	}
}

func TestSignaturePolicyAndPlanCarrySign(t *testing.T) {
	if SignatureRequired(map[string]any{}) {
		t.Fatal("empty policies must not require signatures")
	}
	if SignatureRequired(map[string]any{"signing": map[string]any{"required": "false"}}) {
		t.Fatal("string false must not require signatures")
	}
	if !SignatureRequired(map[string]any{"signing": map[string]any{"required": true}}) {
		t.Fatal("bool true must require signatures")
	}
	if !SignatureRequired(map[string]any{"signing": map[string]any{"required": "TRUE"}}) {
		t.Fatal("string TRUE must require signatures")
	}
}

func TestPlanTargetsCopySignCommand(t *testing.T) {
	profile := &Profile{Release: &ReleaseConfig{Targets: map[string]TargetConfig{
		"darwin-arm64": {Build: "make app", Sign: "codesign --sign demo app", Verify: "codesign --verify app"},
	}}}
	targets, err := profile.PlanTargets(nil, nil)
	if err != nil {
		t.Fatalf("plan targets: %v", err)
	}
	if len(targets) != 1 || targets[0].SignCommand != "codesign --sign demo app" {
		t.Fatalf("sign command not carried: %+v", targets)
	}
}

func TestArtifactSignedRuleCarriesThroughPlan(t *testing.T) {
	raw := []byte(`
version: 1
release:
  targets:
    linux-amd64:
      build: "true"
      sign: "true"
      verify: "true"
      artifacts:
        - role: app-bundle
          glob: dist/app.tar.gz
        - role: debug-symbol
          glob: dist/app.sym
          signed: false
`)
	p, err := ParseProfile(raw)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := p.PlanTargets(nil, nil)
	if err != nil || len(targets) != 1 {
		t.Fatalf("plan targets: %v %v", targets, err)
	}
	rules := targets[0].Artifacts
	if len(rules) != 2 {
		t.Fatalf("rules = %+v", rules)
	}
	if rules[0].Signed != nil {
		t.Fatalf("app-bundle should default to nil (follow target sign), got %v", *rules[0].Signed)
	}
	if rules[1].Signed == nil || *rules[1].Signed {
		t.Fatalf("debug-symbol signed = %v, want explicit false", rules[1].Signed)
	}
}