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
