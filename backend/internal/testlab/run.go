// Package testlab defines immutable test inputs and independently evaluated results.
package testlab

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var ErrInvalid = errors.New("testlab: invalid")
var ErrState = errors.New("testlab: invalid state")
var hash = regexp.MustCompile("^[a-f0-9]{64}$")
var commit = regexp.MustCompile("^[a-f0-9]{40}([a-f0-9]{24})?$")

type Spec struct {
	ProjectID       string   `json:"project_id"`
	SourceSHA       string   `json:"source_sha"`
	ArtifactSHA256  string   `json:"artifact_sha256"`
	Scenario        string   `json:"scenario"`
	ScenarioVersion string   `json:"scenario_version"`
	Platform        string   `json:"platform"`
	RequiredChecks  []string `json:"required_checks"`
}
type Check struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Evidence string `json:"evidence"`
}
type Report struct {
	SourceSHA       string  `json:"source_sha"`
	ArtifactSHA256  string  `json:"artifact_sha256"`
	ScenarioVersion string  `json:"scenario_version"`
	Platform        string  `json:"platform"`
	Checks          []Check `json:"checks"`
}
type Run struct {
	ID         string     `json:"id"`
	Spec       Spec       `json:"spec"`
	Status     string     `json:"status"`
	Report     *Report    `json:"report,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

func (s Spec) Validate() error {
	if strings.TrimSpace(s.ProjectID) == "" || !commit.MatchString(s.SourceSHA) ||
		!hash.MatchString(s.ArtifactSHA256) || strings.TrimSpace(s.Scenario) == "" ||
		strings.TrimSpace(s.ScenarioVersion) == "" || strings.TrimSpace(s.Platform) == "" {
		return fmt.Errorf("%w: project, full source SHA, artifact SHA256, scenario version and platform required", ErrInvalid)
	}
	if len(s.RequiredChecks) == 0 || len(s.RequiredChecks) > 256 {
		return fmt.Errorf("%w: 1..256 required checks expected", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, id := range s.RequiredChecks {
		if strings.TrimSpace(id) == "" || seen[id] {
			return fmt.Errorf("%w: empty or duplicate required check", ErrInvalid)
		}
		seen[id] = true
	}
	return nil
}

// Evaluate never accepts a caller-supplied PASS verdict. Missing evidence or checks
// cannot pass. Input identity mismatch is rejected rather than recorded as a test failure.
func Evaluate(s Spec, r Report) (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	if s.SourceSHA != r.SourceSHA || s.ArtifactSHA256 != r.ArtifactSHA256 ||
		s.ScenarioVersion != r.ScenarioVersion || s.Platform != r.Platform {
		return "", fmt.Errorf("%w: report does not match assigned inputs", ErrInvalid)
	}
	if len(r.Checks) > 512 {
		return "", fmt.Errorf("%w: too many checks", ErrInvalid)
	}
	checks := map[string]Check{}
	for _, c := range r.Checks {
		if strings.TrimSpace(c.ID) == "" {
			return "", fmt.Errorf("%w: empty check id", ErrInvalid)
		}
		if _, ok := checks[c.ID]; ok {
			return "", fmt.Errorf("%w: duplicate check %s", ErrInvalid, c.ID)
		}
		switch c.Status {
		case "PASS", "FAIL", "BLOCKED", "SKIPPED":
		default:
			return "", fmt.Errorf("%w: unknown status", ErrInvalid)
		}
		checks[c.ID] = c
	}
	status := "PASS"
	for _, id := range s.RequiredChecks {
		c, ok := checks[id]
		if ok && c.Status == "FAIL" {
			return "FAIL", nil
		}
		if !ok || c.Status != "PASS" || strings.TrimSpace(c.Evidence) == "" {
			status = "BLOCKED"
		}
	}
	return status, nil
}

// MatchesPassedRun is the pure gate predicate; callers must load the run from a
// trusted store and supply the current release policy, never the runner's policy.
func MatchesPassedRun(run Run, expected Spec) error {
	if run.Status != "PASS" || run.Report == nil || run.FinishedAt == nil {
		return ErrState
	}
	if run.Spec.ProjectID != expected.ProjectID || run.Spec.Scenario != expected.Scenario {
		return ErrInvalid
	}
	verdict, err := Evaluate(expected, *run.Report)
	if err != nil {
		return err
	}
	if verdict != "PASS" {
		return ErrState
	}
	return nil
}
