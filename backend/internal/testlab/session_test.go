package testlab

import (
	"encoding/json"
	"strings"
	"testing"
)

func sessionFixture() SessionSpec {
	return SessionSpec{ProjectID: "p", Title: "story", Kind: "story", Platform: "windows-amd64",
		Story: "# do the thing", TimeoutSeconds: 600, RequiredChecks: []string{"fixed"}}
}

func TestSessionSpecDefaults(t *testing.T) {
	s := SessionSpec{ProjectID: "p", Title: "t", Platform: "windows-amd64", Story: "x"}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.Kind != "" || s.TimeoutSeconds != 0 {
		t.Fatal("Validate must not mutate its value receiver")
	}
}

func TestEvaluateSession(t *testing.T) {
	cases := []struct {
		name      string
		requested string
		checks    []SessionCheck
		required  []string
		want      string
		invalid   bool
	}{
		{"pass with evidence", "PASS", []SessionCheck{{ID: "fixed", Status: "PASS", Evidence: "sha256:a"}}, []string{"fixed"}, "PASS", false},
		{"pass without evidence", "PASS", []SessionCheck{{ID: "fixed", Status: "PASS"}}, []string{"fixed"}, "BLOCKED", false},
		{"missing required", "PASS", nil, []string{"fixed"}, "BLOCKED", false},
		{"required fail wins", "PASS", []SessionCheck{{ID: "fixed", Status: "FAIL"}}, []string{"fixed"}, "FAIL", false},
		{"agent fail", "FAIL", []SessionCheck{{ID: "fixed", Status: "PASS", Evidence: "sha256:a"}}, []string{"fixed"}, "FAIL", false},
		{"cancelled", "CANCELLED", nil, []string{"fixed"}, "CANCELLED", false},
		{"free pass needs evidence", "PASS", []SessionCheck{{ID: "ui", Status: "PASS", Evidence: "sha256:b"}}, nil, "PASS", false},
		{"free pass no checks", "PASS", nil, nil, "BLOCKED", false},
		{"bad verdict", "OK", nil, nil, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := sessionFixture()
			s.RequiredChecks = tc.required
			got, err := EvaluateSession(s, tc.requested, tc.checks)
			if (err != nil) != tc.invalid || got != tc.want {
				t.Fatalf("got %q %v", got, err)
			}
		})
	}
}

func TestSessionCommandValidation(t *testing.T) {
	if err := ValidateSessionCommand("exec", json.RawMessage(`{"executable":"C:\\x.exe"}`)); err != nil {
		t.Fatal(err)
	}
	if ValidateSessionCommand("rm_rf", nil) == nil {
		t.Fatal("unknown command accepted")
	}
	if ValidateSessionCommand("exec", json.RawMessage(`{`)) == nil {
		t.Fatal("invalid JSON accepted")
	}
	if ValidateSessionCommand("exec", json.RawMessage(`{"x":"`+strings.Repeat("a", MaxSessionArgsBytes)+`"}`)) == nil {
		t.Fatal("oversized args accepted")
	}
}

func engineFixture() SessionSpec {
	s := sessionFixture()
	s.Story = ""
	s.Kind = "hotswap"
	s.Mode = "hotswap"
	s.Engine = &EngineSpec{
		Inputs: []EngineInput{
			{Name: "workspace", Kind: "workspace", SHA256: strings.Repeat("a", 64), Unpack: "tar.gz", Target: "workspace"},
			{Name: "stories", Kind: "stories", SHA256: strings.Repeat("b", 64), Unpack: "zip", Target: "stories"},
			{Name: "plan", Kind: "hotswap", SHA256: strings.Repeat("c", 64), Unpack: "none", Target: "plan.json"},
		},
		Stories: []EngineStory{{Input: "stories", Path: "hotswap.md", RequiredChecks: []string{"engine_restored"}}},
		Hotswap: &HotswapSpec{Input: "plan", Path: "plan.json", RestorePolicy: "always"},
		Agent:   EngineAgent{Sandbox: "workspace-write", TimeoutSeconds: 900},
	}
	return s
}

func TestEngineSessionValidation(t *testing.T) {
	s := engineFixture().Normalize()
	if err := s.Validate(); err != nil {
		t.Fatalf("valid engine session rejected: %v", err)
	}
	if s.Engine.Agent.Sandbox != "workspace-write" || s.Engine.Workdir != "workspace" {
		t.Fatalf("normalize lost engine defaults: %+v", s.Engine)
	}
	cases := []struct {
		name   string
		mutate func(*SessionSpec)
	}{
		{"unknown story input", func(s *SessionSpec) { s.Engine.Stories[0].Input = "missing" }},
		{"absolute story path", func(s *SessionSpec) { s.Engine.Stories[0].Path = "C:\\stories\\a.md" }},
		{"escaping target", func(s *SessionSpec) { s.Engine.Inputs[0].Target = "../escape" }},
		{"bad digest", func(s *SessionSpec) { s.Engine.Inputs[0].SHA256 = "abc" }},
		{"bad unpack", func(s *SessionSpec) { s.Engine.Inputs[0].Unpack = "rar" }},
		{"bad restore policy", func(s *SessionSpec) { s.Engine.Hotswap.RestorePolicy = "sometimes" }},
		{"bad sandbox", func(s *SessionSpec) { s.Engine.Agent.Sandbox = "root" }},
		{"mode mismatch", func(s *SessionSpec) { s.Mode = "story" }},
		{"no story at all", func(s *SessionSpec) { s.Engine.Stories = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := engineFixture()
			tc.mutate(&bad)
			if err := bad.Validate(); err == nil {
				t.Fatal("invalid engine session accepted")
			}
		})
	}
}

func TestEvaluateSessionWithFindings(t *testing.T) {
	s := sessionFixture()
	checks := []SessionCheck{{ID: "fixed", Status: "PASS", Evidence: "sha256:" + strings.Repeat("a", 64)}}
	open := []Finding{{ID: "f1", Severity: "blocker", Title: "app crashes", Status: "open"}}
	if got, err := EvaluateSessionWithFindings(s, "PASS", checks, open); err != nil || got != "FAIL" {
		t.Fatalf("open blocker must fail: %q %v", got, err)
	}
	fixed := []Finding{{ID: "f1", Severity: "blocker", Title: "app crashes", Status: "fixed"}}
	if got, err := EvaluateSessionWithFindings(s, "PASS", checks, fixed); err != nil || got != "PASS" {
		t.Fatalf("fixed blocker must pass: %q %v", got, err)
	}
	minor := []Finding{{ID: "f2", Severity: "minor", Title: "label typo"}}
	if got, err := EvaluateSessionWithFindings(s, "PASS", checks, minor); err != nil || got != "PASS" {
		t.Fatalf("minor finding must not fail: %q %v", got, err)
	}
	badEvidence := []Finding{{ID: "f3", Severity: "minor", Title: "x", Evidence: []string{"file.png"}}}
	if _, err := EvaluateSessionWithFindings(s, "PASS", checks, badEvidence); err == nil {
		t.Fatal("non-sha256 finding evidence accepted")
	}
	badSeverity := []Finding{{ID: "f4", Severity: "urgent", Title: "x"}}
	if _, err := EvaluateSessionWithFindings(s, "PASS", checks, badSeverity); err == nil {
		t.Fatal("unknown severity accepted")
	}
}

func TestSessionEventValidate(t *testing.T) {
	good := SessionEvent{Seq: 1, Kind: "report", Message: "submitted PASS"}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	for name, event := range map[string]SessionEvent{
		"zero seq":  {Seq: 0, Kind: "report", Message: "x"},
		"bad kind":  {Seq: 1, Kind: "Report!", Message: "x"},
		"empty":     {Seq: 1, Kind: "report", Message: "  "},
		"oversized": {Seq: 1, Kind: "report", Message: strings.Repeat("a", MaxSessionEventBytes+1)},
	} {
		if err := event.Validate(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
