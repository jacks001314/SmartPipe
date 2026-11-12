// Package releasev2 implements the durable release-control model used by the
// v2 release service, admin API and MCP tools. It deliberately does not own
// build execution or publisher credentials.
package releasev2

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	ErrInvalid     = errors.New("releasev2: invalid request")
	ErrState       = errors.New("releasev2: invalid state")
	ErrPlanExpired = errors.New("releasev2: plan expired")
	ErrPlanBlocked = errors.New("releasev2: plan has blockers")
	ErrIdempotency = errors.New("releasev2: idempotency key already used")
)

type Mode string

const (
	ModeBuildOnly Mode = "build_only"
	ModeStage     Mode = "stage"
	ModePublish   Mode = "publish"
)

func (m Mode) Valid() bool {
	return m == ModeBuildOnly || m == ModeStage || m == ModePublish
}

type PlanStatus string

const (
	PlanPlanned   PlanStatus = "planned"
	PlanConsumed  PlanStatus = "consumed"
	PlanExpired   PlanStatus = "expired"
	PlanCancelled PlanStatus = "cancelled"
)

type RunStatus string

const (
	RunQueued             RunStatus = "queued"
	RunPreparing          RunStatus = "preparing"
	RunBuilding           RunStatus = "building"
	RunVerifying          RunStatus = "verifying"
	RunStaged             RunStatus = "staged"
	RunPublishing         RunStatus = "publishing"
	RunPublished          RunStatus = "published"
	RunPartiallyPublished RunStatus = "partially_published"
	RunBuilt              RunStatus = "built"
	RunFailed             RunStatus = "failed"
	RunBlocked            RunStatus = "blocked"
	RunCancelled          RunStatus = "cancelled"
)

type TargetStatus string

const (
	TargetQueued      TargetStatus = "queued"
	TargetLeased      TargetStatus = "leased"
	TargetBuilding    TargetStatus = "building"
	TargetVerifying   TargetStatus = "verifying"
	TargetUploaded    TargetStatus = "uploaded"
	TargetReady       TargetStatus = "ready"
	TargetFailed      TargetStatus = "failed"
	TargetCancelled   TargetStatus = "cancelled"
	TargetRetryQueued TargetStatus = "retry_queued"
)

type CandidateIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Author string `json:"author,omitempty"`
}

type TargetPlan struct {
	ID            string         `json:"id"`
	OS            string         `json:"os"`
	Arch          string         `json:"arch"`
	ExecutorID    string         `json:"executor_id"`
	Required      bool           `json:"required"`
	BuildCommand  string         `json:"build_command,omitempty"`
	SignCommand   string         `json:"sign_command,omitempty"`
	VerifyCommand string         `json:"verify_command,omitempty"`
	ArtifactRoles []string       `json:"artifact_roles,omitempty"`
	Artifacts     []ArtifactRule `json:"artifacts,omitempty"`
}

// RunRetention 是制品清理需要的最小运行信息：状态、归属与参考时间。
type RunRetention struct {
	Status     RunStatus
	ProjectID  string
	Channel    string
	FinishedAt *time.Time
	UpdatedAt  time.Time
}

type Blocker struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	TargetID string `json:"target_id,omitempty"`
}

type PlanRequest struct {
	ProjectID      string `json:"project_id"`
	RequestedBy    string `json:"requested_by,omitempty"`
	Trigger        string `json:"trigger,omitempty"`
	Mode           Mode   `json:"mode"`
	Version        string `json:"version"`
	BuildVersion   string `json:"build_version,omitempty"`
	Tag            string `json:"tag,omitempty"`
	Channel        string `json:"channel,omitempty"`
	Repo           string `json:"repo,omitempty"`
	Branch         string `json:"branch,omitempty"`
	Commit         string `json:"commit,omitempty"`
	ResolvedCommit string `json:"resolved_commit,omitempty"`
	ProfileSHA256  string `json:"profile_sha256,omitempty"`
	// CloneCredential 是构建机克隆私有仓库时使用的凭据引用名（不是明文）。
	CloneCredential string           `json:"clone_credential,omitempty"`
	Candidates      []CandidateIssue `json:"candidate_issues,omitempty"`
	Targets         []TargetPlan     `json:"targets"`
	Blockers        []Blocker        `json:"blockers,omitempty"`
	NotesMD         string           `json:"notes_md,omitempty"`
	TTL             time.Duration    `json:"ttl,omitempty"`
}

type Plan struct {
	ID              string           `json:"id"`
	ProjectID       string           `json:"project_id"`
	RequestedBy     string           `json:"requested_by"`
	Trigger         string           `json:"trigger"`
	Mode            Mode             `json:"mode"`
	Status          PlanStatus       `json:"status"`
	Version         string           `json:"version"`
	BuildVersion    string           `json:"build_version"`
	Tag             string           `json:"tag"`
	Channel         string           `json:"channel"`
	Repo            string           `json:"repo"`
	SourceBranch    string           `json:"source_branch"`
	SourceCommit    string           `json:"source_commit"`
	ResolvedCommit  string           `json:"resolved_commit"`
	ProfileSHA256   string           `json:"profile_sha256"`
	PlanSHA256      string           `json:"plan_sha256"`
	CloneCredential string           `json:"clone_credential,omitempty"`
	Candidates      []CandidateIssue `json:"candidates"`
	Targets         []TargetPlan     `json:"targets"`
	Blockers        []Blocker        `json:"blockers"`
	NotesMD         string           `json:"notes_md"`
	NotesSHA256     string           `json:"notes_sha256"`
	ExpiresAt       time.Time        `json:"expires_at"`
	CreatedAt       time.Time        `json:"created_at"`
}

type Run struct {
	ID              string     `json:"id"`
	PlanID          string     `json:"plan_id"`
	ProjectID       string     `json:"project_id"`
	Repo            string     `json:"repo"`
	RequestedBy     string     `json:"requested_by"`
	Trigger         string     `json:"trigger"`
	Mode            Mode       `json:"mode"`
	Channel         string     `json:"channel"`
	Status          RunStatus  `json:"status"`
	Version         string     `json:"version"`
	BuildVersion    string     `json:"build_version"`
	Tag             string     `json:"tag"`
	SourceBranch    string     `json:"source_branch"`
	SourceCommit    string     `json:"source_commit"`
	ResolvedCommit  string     `json:"resolved_commit"`
	ProfileSHA256   string     `json:"profile_sha256"`
	PlanSHA256      string     `json:"plan_sha256"`
	IdempotencyKey  string     `json:"idempotency_key,omitempty"`
	CloneCredential string     `json:"clone_credential,omitempty"`
	AgentThreadID   string     `json:"agent_thread_id,omitempty"`
	NotesMD         string     `json:"notes_md"`
	NotesSHA256     string     `json:"notes_sha256"`
	ApprovalState   string     `json:"approval_state"`
	ErrorCode       string     `json:"error_code,omitempty"`
	ErrorMessage    string     `json:"error_message,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type Target struct {
	ID             int64          `json:"id"`
	RunID          string         `json:"run_id"`
	TargetID       string         `json:"target_id"`
	OS             string         `json:"os"`
	Arch           string         `json:"arch"`
	ExecutorID     string         `json:"executor_id"`
	Required       bool           `json:"required"`
	Status         TargetStatus   `json:"status"`
	Attempt        int            `json:"attempt"`
	LeaseOwner     string         `json:"lease_owner,omitempty"`
	LeaseExpiresAt *time.Time     `json:"lease_expires_at,omitempty"`
	BuildCommand   string         `json:"build_command,omitempty"`
	SignCommand    string         `json:"sign_command,omitempty"`
	VerifyCommand  string         `json:"verify_command,omitempty"`
	Artifacts      []ArtifactRule `json:"artifacts,omitempty"`
	ErrorCode      string         `json:"error_code,omitempty"`
	ErrorMessage   string         `json:"error_message,omitempty"`
	StartedAt      *time.Time     `json:"started_at,omitempty"`
	FinishedAt     *time.Time     `json:"finished_at,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

type Artifact struct {
	ID             int64     `json:"id"`
	RunID          string    `json:"run_id"`
	TargetID       string    `json:"target_id"`
	Attempt        int       `json:"attempt"`
	Role           string    `json:"role"`
	Name           string    `json:"name"`
	ContentType    string    `json:"content_type"`
	Size           int64     `json:"size"`
	SHA256         string    `json:"sha256"`
	StorageKey     string    `json:"storage_key"`
	PlatformSigned bool      `json:"platform_signed"`
	ManifestSigned bool      `json:"manifest_signed"`
	CreatedAt      time.Time `json:"created_at"`
}

type Publication struct {
	ID            int64     `json:"id"`
	RunID         string    `json:"run_id"`
	Provider      string    `json:"provider"`
	TargetID      string    `json:"target_id"`
	State         string    `json:"state"`
	ExternalID    string    `json:"external_id"`
	ExternalURL   string    `json:"external_url"`
	RequestSHA256 string    `json:"request_sha256"`
	ErrorMessage  string    `json:"error_message"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Event struct {
	ID        int64          `json:"id"`
	RunID     string         `json:"run_id"`
	Actor     string         `json:"actor"`
	Kind      string         `json:"kind"`
	TargetID  string         `json:"target_id,omitempty"`
	Summary   string         `json:"summary"`
	Detail    map[string]any `json:"detail,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

type Approval struct {
	ID            string     `json:"id"`
	RunID         string     `json:"run_id"`
	Kind          string     `json:"kind"`
	Actor         string     `json:"actor"`
	Reason        string     `json:"reason"`
	ContentSHA256 string     `json:"content_sha256"`
	State         string     `json:"state"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

// ApprovalPolicy reports whether a channel needs a human approval before the
// channel pointer may move, and how long an approval stays valid.
func ApprovalPolicy(policies map[string]any, channel string) (bool, time.Duration) {
	ttl := 2 * time.Hour
	raw, ok := policies["approvals"].(map[string]any)
	if !ok {
		return strings.EqualFold(channel, "stable"), ttl
	}
	if v := strings.TrimSpace(toString(raw["ttl"])); v != "" {
		if parsed, err := time.ParseDuration(v); err == nil && parsed > 0 {
			ttl = parsed
		}
	}
	channels := stringList(raw["required_channels"])
	if len(channels) == 0 {
		return strings.EqualFold(channel, "stable"), ttl
	}
	for _, c := range channels {
		if strings.EqualFold(c, channel) {
			return true, ttl
		}
	}
	return false, ttl
}

// SignatureRequired 报告发版配置是否要求制品在发布前具备平台签名
// （macOS codesign / Windows signtool）。配置写在 release.policies.signing.required。
func SignatureRequired(policies map[string]any) bool {
	raw, ok := policies["signing"].(map[string]any)
	if !ok {
		return false
	}
	switch v := raw["required"].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	}
	return false
}

func toString(v any) string {
	s, _ := v.(string)
	return s
}

func stringList(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s := strings.TrimSpace(toString(item)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

type Executor struct {
	ID              string         `json:"id"`
	OS              string         `json:"os"`
	Arch            string         `json:"arch"`
	Labels          []string       `json:"labels"`
	Capabilities    map[string]any `json:"capabilities"`
	Version         string         `json:"version"`
	Status          string         `json:"status"`
	LastHeartbeatAt *time.Time     `json:"last_heartbeat_at,omitempty"`
	LastError       string         `json:"last_error"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

type Token struct {
	ID         string
	RunID      string
	ProjectID  string
	TokenHash  string
	Scopes     []string
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
	CreatedAt  time.Time
}

var (
	versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$`)
	targetPattern  = regexp.MustCompile(`^(windows|darwin|linux)-(amd64|arm64)$`)
	commitPattern  = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
)

func NewID(prefix string) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(buf)
}

func HashJSON(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func HashText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func NormalizeVersion(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	if v == "" || !versionPattern.MatchString(v) || strings.Contains(v, "..") {
		return "", fmt.Errorf("%w: unsafe version %q", ErrInvalid, raw)
	}
	return v, nil
}

func BuildPlan(req PlanRequest, now time.Time) (*Plan, error) {
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	if req.ProjectID == "" {
		return nil, fmt.Errorf("%w: project_id is required", ErrInvalid)
	}
	if req.Mode == "" {
		req.Mode = ModePublish
	}
	if !req.Mode.Valid() {
		return nil, fmt.Errorf("%w: unsupported mode %q", ErrInvalid, req.Mode)
	}
	version, err := NormalizeVersion(req.Version)
	if err != nil {
		return nil, err
	}
	if req.Channel == "" {
		req.Channel = "stable"
	}
	if req.Trigger == "" {
		req.Trigger = "manual"
	}
	if req.Branch == "" {
		req.Branch = "main"
	}
	buildVersion := strings.TrimSpace(req.BuildVersion)
	if buildVersion == "" {
		buildVersion = version
	}
	tag := strings.TrimSpace(req.Tag)
	if tag == "" {
		tag = "v" + version
	}
	commit := strings.TrimSpace(req.ResolvedCommit)
	if commit == "" {
		commit = strings.TrimSpace(req.Commit)
	}
	if commit != "" && !commitPattern.MatchString(commit) {
		return nil, fmt.Errorf("%w: invalid commit %q", ErrInvalid, commit)
	}
	targets, err := normalizeTargets(req.Targets)
	if err != nil {
		return nil, err
	}
	candidates := append([]CandidateIssue(nil), req.Candidates...)
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Number < candidates[j].Number })
	blockers := append([]Blocker(nil), req.Blockers...)
	ttl := req.TTL
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	now = now.UTC()
	p := &Plan{
		ID: NewID("rplan"), ProjectID: req.ProjectID, RequestedBy: req.RequestedBy,
		Trigger: req.Trigger, Mode: req.Mode, Status: PlanPlanned, Version: version,
		BuildVersion: buildVersion, Tag: tag, Channel: req.Channel, Repo: req.Repo,
		SourceBranch: req.Branch, SourceCommit: req.Commit, ResolvedCommit: commit,
		ProfileSHA256: req.ProfileSHA256, CloneCredential: strings.TrimSpace(req.CloneCredential),
		Candidates: candidates, Targets: targets,
		Blockers: blockers, NotesMD: req.NotesMD, NotesSHA256: HashText(req.NotesMD),
		ExpiresAt: now.Add(ttl), CreatedAt: now,
	}
	hash, err := PlanHash(p)
	if err != nil {
		return nil, err
	}
	p.PlanSHA256 = hash
	return p, nil
}

func normalizeTargets(in []TargetPlan) ([]TargetPlan, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("%w: at least one target is required", ErrInvalid)
	}
	seen := map[string]bool{}
	out := make([]TargetPlan, 0, len(in))
	for _, t := range in {
		t.ID = strings.TrimSpace(t.ID)
		t.OS = strings.ToLower(strings.TrimSpace(t.OS))
		t.Arch = strings.ToLower(strings.TrimSpace(t.Arch))
		t.ExecutorID = strings.TrimSpace(t.ExecutorID)
		if !targetPattern.MatchString(t.ID) {
			return nil, fmt.Errorf("%w: invalid target %q", ErrInvalid, t.ID)
		}
		parts := strings.SplitN(t.ID, "-", 2)
		if t.OS == "" {
			t.OS = parts[0]
		}
		if t.Arch == "" {
			t.Arch = parts[1]
		}
		if t.OS != parts[0] || t.Arch != parts[1] {
			return nil, fmt.Errorf("%w: target %s does not match %s/%s", ErrInvalid, t.ID, t.OS, t.Arch)
		}
		if seen[t.ID] {
			return nil, fmt.Errorf("%w: duplicate target %s", ErrInvalid, t.ID)
		}
		seen[t.ID] = true
		t.Required = true
		t.ArtifactRoles = append([]string(nil), t.ArtifactRoles...)
		sort.Strings(t.ArtifactRoles)
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func PlanHash(p *Plan) (string, error) {
	if p == nil {
		return "", fmt.Errorf("%w: plan is nil", ErrInvalid)
	}
	view := struct {
		ProjectID      string           `json:"project_id"`
		Mode           Mode             `json:"mode"`
		Version        string           `json:"version"`
		BuildVersion   string           `json:"build_version"`
		Tag            string           `json:"tag"`
		Channel        string           `json:"channel"`
		Repo           string           `json:"repo"`
		SourceBranch   string           `json:"source_branch"`
		SourceCommit   string           `json:"source_commit"`
		ResolvedCommit string           `json:"resolved_commit"`
		ProfileSHA256  string           `json:"profile_sha256"`
		Candidates     []CandidateIssue `json:"candidates"`
		Targets        []TargetPlan     `json:"targets"`
		NotesSHA256    string           `json:"notes_sha256"`
	}{
		ProjectID: p.ProjectID, Mode: p.Mode, Version: p.Version, BuildVersion: p.BuildVersion,
		Tag: p.Tag, Channel: p.Channel, Repo: p.Repo, SourceBranch: p.SourceBranch,
		SourceCommit: p.SourceCommit, ResolvedCommit: p.ResolvedCommit,
		ProfileSHA256: p.ProfileSHA256, Candidates: p.Candidates, Targets: p.Targets,
		NotesSHA256: p.NotesSHA256,
	}
	return HashJSON(view)
}

func (p *Plan) ValidateForRun(now time.Time) error {
	if p == nil {
		return fmt.Errorf("%w: plan is required", ErrInvalid)
	}
	if p.Status != PlanPlanned {
		return fmt.Errorf("%w: plan status is %s", ErrState, p.Status)
	}
	if !p.ExpiresAt.IsZero() && now.After(p.ExpiresAt) {
		return ErrPlanExpired
	}
	if len(p.Blockers) > 0 {
		return ErrPlanBlocked
	}
	return nil
}

func RunFromPlan(plan *Plan, idempotencyKey, requestedBy string) (*Run, []Target, error) {
	if plan == nil {
		return nil, nil, fmt.Errorf("%w: plan is required", ErrInvalid)
	}
	now := time.Now().UTC()
	run := &Run{
		ID: NewID("rrun"), PlanID: plan.ID, ProjectID: plan.ProjectID, Repo: plan.Repo,
		RequestedBy: firstNonEmpty(requestedBy, plan.RequestedBy), Trigger: plan.Trigger,
		Mode: plan.Mode, Channel: plan.Channel, Status: RunQueued, Version: plan.Version,
		BuildVersion: plan.BuildVersion, Tag: plan.Tag, SourceBranch: plan.SourceBranch,
		SourceCommit: plan.SourceCommit, ResolvedCommit: plan.ResolvedCommit,
		ProfileSHA256: plan.ProfileSHA256, PlanSHA256: plan.PlanSHA256, CloneCredential: plan.CloneCredential,
		IdempotencyKey: strings.TrimSpace(idempotencyKey), NotesMD: plan.NotesMD,
		NotesSHA256: plan.NotesSHA256, CreatedAt: now, UpdatedAt: now,
	}
	targets := make([]Target, 0, len(plan.Targets))
	for _, t := range plan.Targets {
		targets = append(targets, Target{
			RunID: run.ID, TargetID: t.ID, OS: t.OS, Arch: t.Arch, ExecutorID: t.ExecutorID,
			Required: t.Required, Status: TargetQueued, Attempt: 1,
			BuildCommand: t.BuildCommand, SignCommand: t.SignCommand, VerifyCommand: t.VerifyCommand,
			Artifacts: t.Artifacts, CreatedAt: now, UpdatedAt: now,
		})
	}
	return run, targets, nil
}

func CanTransition(from, to RunStatus) bool {
	allowed := map[RunStatus][]RunStatus{
		RunQueued:             {RunPreparing, RunBlocked, RunCancelled, RunFailed},
		RunPreparing:          {RunBuilding, RunBlocked, RunCancelled, RunFailed},
		RunBuilding:           {RunVerifying, RunStaged, RunBuilt, RunBlocked, RunCancelled, RunFailed},
		RunVerifying:          {RunStaged, RunBuilt, RunBlocked, RunCancelled, RunFailed},
		RunStaged:             {RunPublishing, RunBlocked, RunCancelled, RunFailed},
		RunPublishing:         {RunPublished, RunPartiallyPublished, RunBlocked, RunFailed},
		RunPartiallyPublished: {RunPublishing, RunPublished, RunBlocked, RunFailed},
		RunBlocked:            {RunQueued, RunPreparing, RunBuilding, RunCancelled, RunFailed},
		RunFailed:             {RunPreparing, RunCancelled},
		RunBuilt:              {RunPublishing, RunCancelled},
	}
	for _, candidate := range allowed[from] {
		if candidate == to {
			return true
		}
	}
	return false
}

func TerminalRunStatus(s RunStatus) bool {
	switch s {
	case RunPublished, RunBuilt, RunFailed, RunCancelled:
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// NewAgentToken creates a one-time random token. Only the returned hash should
// be persisted; the raw value is shown once to the runtime that starts the
// Release Agent.
func NewAgentToken(runID, projectID string, scopes []string, ttl time.Duration, now time.Time) (string, Token, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(projectID) == "" {
		return "", Token{}, fmt.Errorf("%w: run_id and project_id are required", ErrInvalid)
	}
	if ttl <= 0 {
		ttl = 4 * time.Hour
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", Token{}, fmt.Errorf("releasev2: create token: %w", err)
	}
	raw := "rmt_" + hex.EncodeToString(buf)
	now = now.UTC()
	token := Token{
		ID: NewID("rtok"), RunID: strings.TrimSpace(runID), ProjectID: strings.TrimSpace(projectID),
		TokenHash: HashText(raw), Scopes: append([]string(nil), scopes...), ExpiresAt: now.Add(ttl), CreatedAt: now,
	}
	sort.Strings(token.Scopes)
	return raw, token, nil
}

func HasScope(scopes []string, wanted string) bool {
	wanted = strings.TrimSpace(wanted)
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == wanted || strings.HasSuffix(scope, ".*") && strings.HasPrefix(wanted, strings.TrimSuffix(scope, "*")) {
			return true
		}
	}
	return false
}
