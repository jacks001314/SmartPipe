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

	MaxEngineInputs      = 16
	MaxEngineStories     = 32
	MaxEngineEnv         = 32
	MaxSessionFindings   = 128
	MaxFindingEvidence   = 16
	MaxEngineInputTarget = 96
	MaxSessionEventBytes = 4000
	MaxSessionEvents     = 512
)

var sessionKinds = map[string]bool{"story": true, "bug": true, "explore": true, "hotswap": true, "gate": true}
var eventKindPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
var windowsAbsPath = regexp.MustCompile(`^[a-zA-Z]:[\\/]`)

const (
	SessionModeStory   = "story"
	SessionModeBug     = "bug"
	SessionModeExplore = "explore"
	SessionModeHotswap = "hotswap"
	SessionModeGate    = "gate"
)

// EngineInput is a blob that the SmartPipe scheduler has already prepared and
// uploaded. The engine only downloads, verifies and unpacks it; it never pulls
// source from Git itself.
type EngineInput struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes,omitempty"`
	Unpack string `json:"unpack,omitempty"`
	Target string `json:"target,omitempty"`
}

// EngineStory names a Markdown story inside one of the uploaded inputs.
type EngineStory struct {
	Input          string   `json:"input"`
	Path           string   `json:"path"`
	Title          string   `json:"title,omitempty"`
	RequiredChecks []string `json:"required_checks,omitempty"`
}

// HotswapSpec points at a hotswap plan file inside an uploaded input.
type HotswapSpec struct {
	Input         string `json:"input"`
	Path          string `json:"path"`
	RestorePolicy string `json:"restore_policy,omitempty"`
}

// EngineAgent bounds how the local Codex agent is started on the test machine.
type EngineAgent struct {
	Command        string            `json:"command,omitempty"`
	Model          string            `json:"model,omitempty"`
	Sandbox        string            `json:"sandbox,omitempty"`
	Args           []string          `json:"args,omitempty"`
	ExtraArgs      []string          `json:"extra_args,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
}

// EngineSpec is the engine-facing half of a session. Legacy sessions leave it
// nil and keep using the remote command proxy.
type EngineSpec struct {
	Inputs  []EngineInput     `json:"inputs,omitempty"`
	Stories []EngineStory     `json:"stories,omitempty"`
	Hotswap *HotswapSpec      `json:"hotswap,omitempty"`
	Agent   EngineAgent       `json:"agent,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Workdir string            `json:"workdir,omitempty"`
}

// Finding is one defect observed by the test agent. Only a blocker prevents a
// PASS; everything else is recorded for the reviewer.
type Finding struct {
	ID       string   `json:"id"`
	Severity string   `json:"severity"`
	Status   string   `json:"status,omitempty"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail,omitempty"`
	Repro    string   `json:"repro,omitempty"`
	Expected string   `json:"expected,omitempty"`
	Actual   string   `json:"actual,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
	CheckRef string   `json:"check_ref,omitempty"`
}

// SessionSpec is the immutable description of one agent-driven test session.
// Story is the Markdown text the agent reads; StoryRef is provenance only.
type SessionSpec struct {
	ProjectID      string            `json:"project_id"`
	Title          string            `json:"title"`
	Kind           string            `json:"kind"`
	Mode           string            `json:"mode,omitempty"`
	Environment    string            `json:"environment"`
	Platform       string            `json:"platform"`
	StoryRef       string            `json:"story_ref,omitempty"`
	Story          string            `json:"story"`
	Inputs         map[string]string `json:"inputs,omitempty"`
	RequiredChecks []string          `json:"required_checks,omitempty"`
	Events         []StoryEvent      `json:"events,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	WorkDir        string            `json:"work_dir,omitempty"`
	Engine         *EngineSpec       `json:"engine,omitempty"`
}

type SessionCheck struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Evidence  string    `json:"evidence,omitempty"`
	Note      string    `json:"note,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SessionEvent is one step of the engine timeline shown in the console: claim,
// story load, hotswap, check, finding, report and lifecycle messages. Agent
// transcripts stay out of this table and are kept as evidence instead.
type SessionEvent struct {
	Seq     int       `json:"seq"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

// ValidateSessionEvent rejects events that would break the console.
func (e SessionEvent) Validate() error {
	if e.Seq < 1 || e.Seq > 1_000_000 {
		return fmt.Errorf("%w: event seq out of range", ErrInvalid)
	}
	if !eventKindPattern.MatchString(e.Kind) {
		return fmt.Errorf("%w: invalid event kind", ErrInvalid)
	}
	if strings.TrimSpace(e.Message) == "" || len(e.Message) > MaxSessionEventBytes {
		return fmt.Errorf("%w: event message is required and limited to %d bytes", ErrInvalid, MaxSessionEventBytes)
	}
	return nil
}

type Session struct {
	ID         string         `json:"id"`
	Spec       SessionSpec    `json:"spec"`
	Status     string         `json:"status"`
	MachineID  string         `json:"machine_id,omitempty"`
	Verdict    string         `json:"verdict,omitempty"`
	Summary    string         `json:"summary,omitempty"`
	Checks     []SessionCheck `json:"checks,omitempty"`
	Findings   []Finding      `json:"findings,omitempty"`
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
	kind, mode := s.Kind, s.Mode
	if kind == "" {
		kind = SessionModeStory
	}
	if mode == "" {
		mode = kind
	}
	if kind != mode {
		return fmt.Errorf("%w: kind and mode must match", ErrInvalid)
	}
	if !sessionKinds[mode] {
		return fmt.Errorf("%w: mode must be story, bug, explore, hotswap or gate", ErrInvalid)
	}
	if strings.TrimSpace(s.Platform) == "" {
		return fmt.Errorf("%w: platform is required", ErrInvalid)
	}
	if len(s.Story) > MaxStoryBytes {
		return fmt.Errorf("%w: story is limited to %d bytes", ErrInvalid, MaxStoryBytes)
	}
	if strings.TrimSpace(s.Story) == "" && (s.Engine == nil || len(s.Engine.Stories) == 0) {
		return fmt.Errorf("%w: story is required unless engine.stories names a story file", ErrInvalid)
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
	if err := ValidateEvents(s.Events); err != nil {
		return err
	}
	for _, event := range s.Events {
		if !seen[event.Method] {
			return fmt.Errorf("%w: event %q method %q is not a required check", ErrInvalid, event.ID, event.Method)
		}
	}
	if s.Engine != nil {
		if err := s.Engine.validate(); err != nil {
			return err
		}
	}
	return nil
}

// Normalize applies the defaults Validate assumes. Callers persist the result.
func (s SessionSpec) Normalize() SessionSpec {
	if s.Kind == "" && s.Mode == "" {
		s.Kind = SessionModeStory
	}
	if s.Mode == "" {
		s.Mode = s.Kind
	}
	if s.Kind == "" {
		s.Kind = s.Mode
	}
	if s.TimeoutSeconds == 0 {
		s.TimeoutSeconds = 30 * 60
	}
	if s.Engine != nil {
		if s.Engine.Agent.Sandbox == "" {
			s.Engine.Agent.Sandbox = "danger-full-access"
		}
		if s.Engine.Hotswap != nil && s.Engine.Hotswap.RestorePolicy == "" {
			s.Engine.Hotswap.RestorePolicy = "always"
		}
		if strings.TrimSpace(s.Engine.Workdir) == "" {
			s.Engine.Workdir = "workspace"
		}
	}
	return s
}

var engineInputKind = map[string]bool{"workspace": true, "artifacts": true, "stories": true, "hotswap": true, "file": true}
var engineUnpack = map[string]bool{"": true, "tar.gz": true, "zip": true, "none": true}
var agentSandbox = map[string]bool{"read-only": true, "workspace-write": true, "danger-full-access": true}

func (e EngineSpec) validate() error {
	if len(e.Inputs) > MaxEngineInputs {
		return fmt.Errorf("%w: too many engine inputs", ErrInvalid)
	}
	names := map[string]bool{}
	for _, in := range e.Inputs {
		if !engineInputNamePattern.MatchString(in.Name) || names[in.Name] {
			return fmt.Errorf("%w: invalid or duplicate engine input name", ErrInvalid)
		}
		names[in.Name] = true
		if !engineInputKind[in.Kind] {
			return fmt.Errorf("%w: unsupported engine input kind %q", ErrInvalid, in.Kind)
		}
		if !hash.MatchString(in.SHA256) {
			return fmt.Errorf("%w: engine input %s needs a sha256", ErrInvalid, in.Name)
		}
		if in.Bytes < 0 {
			return fmt.Errorf("%w: engine input %s has a negative size", ErrInvalid, in.Name)
		}
		if !engineUnpack[in.Unpack] {
			return fmt.Errorf("%w: unsupported unpack %q", ErrInvalid, in.Unpack)
		}
		if in.Target != "" && (!safeRelativePath(in.Target) || len(in.Target) > MaxEngineInputTarget) {
			return fmt.Errorf("%w: engine input target must be a short relative path", ErrInvalid)
		}
	}
	if len(e.Stories) > MaxEngineStories {
		return fmt.Errorf("%w: too many stories", ErrInvalid)
	}
	for _, story := range e.Stories {
		if !names[story.Input] {
			return fmt.Errorf("%w: story references unknown input %q", ErrInvalid, story.Input)
		}
		if !safeRelativePath(story.Path) || len(story.Path) > 256 {
			return fmt.Errorf("%w: story path must be relative", ErrInvalid)
		}
		if len(story.RequiredChecks) > MaxSessionChecks {
			return fmt.Errorf("%w: too many story checks", ErrInvalid)
		}
		for _, id := range story.RequiredChecks {
			if !validCheckID(id) {
				return fmt.Errorf("%w: invalid story check id %q", ErrInvalid, id)
			}
		}
	}
	if e.Hotswap != nil {
		if !names[e.Hotswap.Input] {
			return fmt.Errorf("%w: hotswap references unknown input", ErrInvalid)
		}
		if !safeRelativePath(e.Hotswap.Path) || len(e.Hotswap.Path) > 256 {
			return fmt.Errorf("%w: hotswap path must be relative", ErrInvalid)
		}
		switch e.Hotswap.RestorePolicy {
		case "", "always", "on_failure", "never":
		default:
			return fmt.Errorf("%w: restore_policy must be always, on_failure or never", ErrInvalid)
		}
	}
	if e.Agent.Sandbox != "" && !agentSandbox[e.Agent.Sandbox] {
		return fmt.Errorf("%w: unsupported agent sandbox", ErrInvalid)
	}
	if e.Agent.TimeoutSeconds != 0 && (e.Agent.TimeoutSeconds < MinSessionTimeout || e.Agent.TimeoutSeconds > MaxSessionTimeout) {
		return fmt.Errorf("%w: agent timeout out of range", ErrInvalid)
	}
	if len(e.Agent.Args) > 32 || len(e.Agent.ExtraArgs) > 32 {
		return fmt.Errorf("%w: too many agent arguments", ErrInvalid)
	}
	for _, arg := range append(append([]string{}, e.Agent.Args...), e.Agent.ExtraArgs...) {
		if len(arg) > 2048 {
			return fmt.Errorf("%w: agent argument too long", ErrInvalid)
		}
	}
	if len(e.Env) > MaxEngineEnv || len(e.Agent.Env) > MaxEngineEnv {
		return fmt.Errorf("%w: too many environment entries", ErrInvalid)
	}
	for _, env := range []map[string]string{e.Env, e.Agent.Env} {
		for key, value := range env {
			if !envNamePattern.MatchString(key) || len(value) > 4096 {
				return fmt.Errorf("%w: invalid environment entry", ErrInvalid)
			}
		}
	}
	return nil
}

var engineInputNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

func safeRelativePath(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "\\") || windowsAbsPath.MatchString(value) {
		return false
	}
	if strings.Contains(value, "//") || strings.Contains(value, `\\`) {
		return false
	}
	for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." || part == "." || part == "" {
			return false
		}
	}
	return true
}

var checkIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,80}$`)

func validCheckID(id string) bool { return checkIDPattern.MatchString(id) }

// EvaluateSession turns the agent's requested verdict plus recorded checks into
// a terminal status. The agent cannot pass without evidence, and required
// checks always take precedence over a self-declared PASS.
func EvaluateSession(spec SessionSpec, requested string, checks []SessionCheck) (string, error) {
	return EvaluateSessionWithFindings(spec, requested, checks, nil)
}

// EvaluateSessionWithFindings keeps the check-based verdict and adds the rule
// that an open blocker finding can never produce a PASS.
func EvaluateSessionWithFindings(spec SessionSpec, requested string, checks []SessionCheck, findings []Finding) (string, error) {
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
	if len(findings) > MaxSessionFindings {
		return "", fmt.Errorf("%w: too many findings", ErrInvalid)
	}
	blocker := false
	for _, f := range findings {
		if err := f.Validate(); err != nil {
			return "", err
		}
		if f.Severity == "blocker" && f.Status != "fixed" && f.Status != "wontfix" {
			blocker = true
		}
	}
	verdict, err := evaluateChecks(spec, requested, checks)
	if err != nil {
		return "", err
	}
	if blocker && verdict == SessionPass {
		return SessionFail, nil
	}
	return verdict, nil
}

func evaluateChecks(spec SessionSpec, requested string, checks []SessionCheck) (string, error) {
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

var findingSeverity = map[string]bool{"blocker": true, "critical": true, "major": true, "minor": true, "info": true}
var findingStatus = map[string]bool{"": true, "open": true, "observed": true, "fixed": true, "wontfix": true}

func (f Finding) Validate() error {
	if !validCheckID(f.ID) {
		return fmt.Errorf("%w: invalid finding id", ErrInvalid)
	}
	if !findingSeverity[f.Severity] {
		return fmt.Errorf("%w: finding severity must be blocker, critical, major, minor or info", ErrInvalid)
	}
	if !findingStatus[f.Status] {
		return fmt.Errorf("%w: unsupported finding status", ErrInvalid)
	}
	if strings.TrimSpace(f.Title) == "" || len(f.Title) > 200 {
		return fmt.Errorf("%w: finding title is required and limited to 200 bytes", ErrInvalid)
	}
	if len(f.Detail) > 8000 || len(f.Repro) > 4000 || len(f.Expected) > 2000 || len(f.Actual) > 2000 {
		return fmt.Errorf("%w: finding text too long", ErrInvalid)
	}
	if len(f.Evidence) > MaxFindingEvidence {
		return fmt.Errorf("%w: too many finding evidence references", ErrInvalid)
	}
	for _, ref := range f.Evidence {
		if !strings.HasPrefix(ref, "sha256:") || !hash.MatchString(strings.TrimPrefix(ref, "sha256:")) {
			return fmt.Errorf("%w: finding evidence must be sha256 references", ErrInvalid)
		}
	}
	if f.CheckRef != "" && !validCheckID(f.CheckRef) {
		return fmt.Errorf("%w: invalid finding check_ref", ErrInvalid)
	}
	return nil
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
