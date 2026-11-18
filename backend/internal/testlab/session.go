package testlab

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Session states. A session is agent-evaluated: the runner owns the lease and
// cleanup, while the agent records checks and asks for a verdict.
const (
	SessionQueued  = "QUEUED"
	SessionRunning = "RUNNING"
	SessionPass    = "PASS"
	SessionFail    = "FAIL"
	SessionBlocked = "BLOCKED"
	SessionCancel  = "CANCELLED"
)

const (
	MaxStoryBytes       = 256 << 10
	MaxSessionInputs    = 64
	MaxSessionTimeout   = 4 * 60 * 60
	MinSessionTimeout   = 60
	MaxSessionChecks    = 256
	MaxSessionArgsBytes = 64 << 10
)

var sessionKinds = map[string]bool{"story": true, "bug": true, "explore": true}
var windowsAbsPath = regexp.MustCompile(`^[a-zA-Z]:[\\/]`)

// SessionSpec is the immutable description of one agent-driven test session.
// Story is the Markdown text the agent reads; StoryRef is provenance only.
type SessionSpec struct {
	ProjectID      string            `json:"project_id"`
	Title          string            `json:"title"`
	Kind           string            `json:"kind"`
	Environment    string            `json:"environment"`
	Platform       string            `json:"platform"`
	StoryRef       string            `json:"story_ref,omitempty"`
	Story          string            `json:"story"`
	Inputs         map[string]string `json:"inputs,omitempty"`
	RequiredChecks []string          `json:"required_checks,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	WorkDir        string            `json:"work_dir,omitempty"`
}

type SessionCheck struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Evidence  string    `json:"evidence,omitempty"`
	Note      string    `json:"note,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Session struct {
	ID         string         `json:"id"`
	Spec       SessionSpec    `json:"spec"`
	Status     string         `json:"status"`
	MachineID  string         `json:"machine_id,omitempty"`
	Verdict    string         `json:"verdict,omitempty"`
	Summary    string         `json:"summary,omitempty"`
	Checks     []SessionCheck `json:"checks,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	StartedAt  *time.Time     `json:"started_at,omitempty"`
	FinishedAt *time.Time     `json:"finished_at,omitempty"`
	Deadline   *time.Time     `json:"deadline,omitempty"`
}

type SessionCommand struct {
	ID         string          `json:"id"`
	Seq        int64           `json:"seq"`
	Kind       string          `json:"kind"`
	Args       json.RawMessage `json:"args"`
	Status     string          `json:"status"`
	Result     json.RawMessage `json:"result,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	StartedAt  *time.Time      `json:"started_at,omitempty"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
}

type SessionLease struct {
	SessionID  string    `json:"session_id"`
	MachineID  string    `json:"machine_id"`
	Generation int64     `json:"generation"`
	ExpiresAt  time.Time `json:"expires_at"`
	Deadline   time.Time `json:"deadline"`
	Session    *Session  `json:"session"`
}

func (s SessionSpec) Validate() error {
	if strings.TrimSpace(s.ProjectID) == "" {
		return fmt.Errorf("%w: project is required", ErrInvalid)
	}
	if strings.TrimSpace(s.Title) == "" || len(s.Title) > 200 {
		return fmt.Errorf("%w: title is required and limited to 200 bytes", ErrInvalid)
	}
	if s.Kind == "" {
		s.Kind = "story"
	}
	if !sessionKinds[s.Kind] {
		return fmt.Errorf("%w: kind must be story, bug or explore", ErrInvalid)
	}
	if strings.TrimSpace(s.Platform) == "" {
		return fmt.Errorf("%w: platform is required", ErrInvalid)
	}
	if strings.TrimSpace(s.Story) == "" || len(s.Story) > MaxStoryBytes {
		return fmt.Errorf("%w: story is required and limited to %d bytes", ErrInvalid, MaxStoryBytes)
	}
	if s.TimeoutSeconds == 0 {
		s.TimeoutSeconds = 30 * 60
	}
	if s.TimeoutSeconds < MinSessionTimeout || s.TimeoutSeconds > MaxSessionTimeout {
		return fmt.Errorf("%w: timeout_seconds must be %d..%d", ErrInvalid, MinSessionTimeout, MaxSessionTimeout)
	}
	if s.WorkDir != "" && !strings.HasPrefix(s.WorkDir, "/") && !windowsAbsPath.MatchString(s.WorkDir) {
		return fmt.Errorf("%w: work_dir must be absolute", ErrInvalid)
	}
	if len(s.Inputs) > MaxSessionInputs {
		return fmt.Errorf("%w: too many inputs", ErrInvalid)
	}
	for key, value := range s.Inputs {
		if strings.TrimSpace(key) == "" || len(key) > 64 || len(value) > 4096 {
			return fmt.Errorf("%w: invalid input", ErrInvalid)
		}
	}
	if len(s.RequiredChecks) > MaxSessionChecks {
		return fmt.Errorf("%w: too many required checks", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, id := range s.RequiredChecks {
		if !validCheckID(id) || seen[id] {
			return fmt.Errorf("%w: empty, invalid or duplicate required check", ErrInvalid)
		}
		seen[id] = true
	}
	return nil
}

var checkIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,80}$`)

func validCheckID(id string) bool { return checkIDPattern.MatchString(id) }

// EvaluateSession turns the agent's requested verdict plus recorded checks into
// a terminal status. The agent cannot pass without evidence, and required
// checks always take precedence over a self-declared PASS.
func EvaluateSession(spec SessionSpec, requested string, checks []SessionCheck) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	switch requested {
	case SessionPass, SessionFail, SessionBlocked, SessionCancel:
	default:
		return "", fmt.Errorf("%w: verdict must be PASS, FAIL, BLOCKED or CANCELLED", ErrInvalid)
	}
	if requested == SessionCancel {
		return SessionCancel, nil
	}
	byID := map[string]SessionCheck{}
	for _, c := range checks {
		if !validCheckID(c.ID) {
			return "", fmt.Errorf("%w: invalid check id", ErrInvalid)
		}
		switch c.Status {
		case SessionPass, SessionFail, SessionBlocked, "SKIPPED":
		default:
			return "", fmt.Errorf("%w: unknown check status %q", ErrInvalid, c.Status)
		}
		byID[c.ID] = c
	}
	for _, id := range spec.RequiredChecks {
		c, ok := byID[id]
		if ok && c.Status == SessionFail {
			return SessionFail, nil
		}
		if !ok || c.Status != SessionPass || strings.TrimSpace(c.Evidence) == "" {
			if requested == SessionFail {
				return SessionFail, nil
			}
			return SessionBlocked, nil
		}
	}
	if requested == SessionFail || requested == SessionBlocked {
		return requested, nil
	}
	// PASS needs at least one independently observable proof. With required
	// checks the loop above already enforced it for every required check.
	if len(spec.RequiredChecks) == 0 {
		for _, c := range checks {
			if c.Status == SessionPass && strings.TrimSpace(c.Evidence) != "" {
				return SessionPass, nil
			}
		}
		return SessionBlocked, nil
	}
	return SessionPass, nil
}

var sessionCommandKinds = map[string]bool{
	"exec": true, "shell": true, "read_file": true, "write_file": true,
	"list": true, "http": true, "processes": true, "start_process": true,
	"stop_process": true, "screenshot": true, "upload_file": true, "sleep": true, "finish": true,
}

// ValidateSessionCommand bounds what an agent can ask the runner to do. Path and
// executable confinement is enforced again on the runner, which owns the real
// filesystem and process table.
func ValidateSessionCommand(kind string, args json.RawMessage) error {
	if !sessionCommandKinds[kind] {
		return fmt.Errorf("%w: unsupported command %q", ErrInvalid, kind)
	}
	if len(args) > MaxSessionArgsBytes {
		return fmt.Errorf("%w: command args too large", ErrInvalid)
	}
	if len(args) > 0 && !json.Valid(args) {
		return fmt.Errorf("%w: command args must be JSON", ErrInvalid)
	}
	return nil
}
