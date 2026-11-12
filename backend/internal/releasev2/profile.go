package releasev2

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var ErrNoReleaseProfile = errors.New("releasev2: release profile is missing")

type Profile struct {
	Version int            `yaml:"version"`
	Release *ReleaseConfig `yaml:"release"`
}

type ReleaseConfig struct {
	Source     SourceConfig              `yaml:"source"`
	Versioning VersioningConfig          `yaml:"versioning"`
	Notes      NotesConfig               `yaml:"notes"`
	Targets    map[string]TargetConfig   `yaml:"targets"`
	Publishing map[string]map[string]any `yaml:"publishing"`
	Policies   map[string]any            `yaml:"policies"`
}

type SourceConfig struct {
	Branch               string `yaml:"branch"`
	RequireCleanCheckout bool   `yaml:"require_clean_checkout"`
	// Credential 是私有仓库的凭据引用名；构建机用 SMARTPIPE_CRED_<REF> 解析明文。
	Credential string `yaml:"credential"`
}

type VersioningConfig struct {
	Scheme          string   `yaml:"scheme"`
	Source          string   `yaml:"source"`
	DefaultBump     string   `yaml:"default_bump"`
	TagTemplate     string   `yaml:"tag_template"`
	Files           []string `yaml:"files"`
	ValidateCommand string   `yaml:"validate_command"`
}

type NotesConfig struct {
	Path     string   `yaml:"path"`
	Required bool     `yaml:"required"`
	Sources  []string `yaml:"sources"`
}

type TargetConfig struct {
	OS       string `yaml:"os"`
	Arch     string `yaml:"arch"`
	Executor string `yaml:"executor"`
	Timeout  string `yaml:"timeout"`
	Build    string `yaml:"build"`
	// Sign 是构建成功后、校验与上传之前执行的一次性命令（例如 codesign / signtool）。
	Sign      string         `yaml:"sign"`
	Verify    string         `yaml:"verify"`
	Artifacts []ArtifactRule `yaml:"artifacts"`
}

type ArtifactRule struct {
	Role        string `yaml:"role" json:"role"`
	Kind        string `yaml:"kind" json:"kind,omitempty"`
	Glob        string `yaml:"glob" json:"glob"`
	ContentType string `yaml:"content_type" json:"content_type,omitempty"`
	Required    bool   `yaml:"required" json:"required"`
	// Signed 声明该角色的制品是否被 sign 步骤覆盖；nil 表示跟随目标：配了 sign 即视为已签名。
	Signed *bool `yaml:"signed" json:"signed,omitempty"`
}

func ParseProfile(raw []byte) (*Profile, error) {
	var p Profile
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if p.Version == 0 {
		p.Version = 1
	}
	if p.Release == nil {
		return &p, ErrNoReleaseProfile
	}
	if err := p.Release.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

func LoadProfile(path string) (*Profile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("releasev2: read profile: %w", err)
	}
	return ParseProfile(raw)
}

func (c *ReleaseConfig) Validate() error {
	if c == nil {
		return ErrNoReleaseProfile
	}
	if len(c.Targets) == 0 {
		return fmt.Errorf("%w: release.targets is empty", ErrInvalid)
	}
	for id, t := range c.Targets {
		if !targetPattern.MatchString(id) {
			return fmt.Errorf("%w: invalid target id %q", ErrInvalid, id)
		}
		osName, arch := splitTargetID(id)
		if t.OS != "" && strings.ToLower(strings.TrimSpace(t.OS)) != osName {
			return fmt.Errorf("%w: target %s os mismatch", ErrInvalid, id)
		}
		if t.Arch != "" && strings.ToLower(strings.TrimSpace(t.Arch)) != arch {
			return fmt.Errorf("%w: target %s arch mismatch", ErrInvalid, id)
		}
		if strings.TrimSpace(t.Build) == "" {
			return fmt.Errorf("%w: target %s has no build command", ErrInvalid, id)
		}
		if strings.TrimSpace(t.Verify) == "" {
			return fmt.Errorf("%w: target %s has no verify command", ErrInvalid, id)
		}
		if strings.TrimSpace(t.Timeout) != "" {
			if _, err := time.ParseDuration(strings.TrimSpace(t.Timeout)); err != nil {
				return fmt.Errorf("%w: target %s timeout: %v", ErrInvalid, id, err)
			}
		}
		roleSeen := map[string]bool{}
		for _, a := range t.Artifacts {
			role := strings.TrimSpace(a.Role)
			if role == "" || roleSeen[role] {
				return fmt.Errorf("%w: target %s has empty or duplicate artifact role %q", ErrInvalid, id, role)
			}
			roleSeen[role] = true
			if strings.TrimSpace(a.Glob) == "" {
				return fmt.Errorf("%w: target %s artifact %s has no glob", ErrInvalid, id, role)
			}
		}
	}
	return nil
}

func (p *Profile) ProfileSHA256() (string, error) {
	if p == nil {
		return "", fmt.Errorf("%w: profile is nil", ErrInvalid)
	}
	raw, err := yaml.Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (p *Profile) TargetIDs() []string {
	if p == nil || p.Release == nil {
		return nil
	}
	out := make([]string, 0, len(p.Release.Targets))
	for id := range p.Release.Targets {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (p *Profile) PlanTargets(executorOverrides map[string]string, only []string) ([]TargetPlan, error) {
	if p == nil || p.Release == nil {
		return nil, ErrNoReleaseProfile
	}
	ids := p.TargetIDs()
	if len(only) > 0 {
		allowed := map[string]bool{}
		for _, id := range only {
			allowed[strings.TrimSpace(id)] = true
		}
		filtered := ids[:0]
		for _, id := range ids {
			if allowed[id] {
				filtered = append(filtered, id)
			}
		}
		for id := range allowed {
			if _, ok := p.Release.Targets[id]; !ok {
				return nil, fmt.Errorf("%w: target %s is not configured", ErrInvalid, id)
			}
		}
		ids = filtered
	}
	out := make([]TargetPlan, 0, len(ids))
	for _, id := range ids {
		cfg := p.Release.Targets[id]
		osName, arch := splitTargetID(id)
		executor := strings.TrimSpace(cfg.Executor)
		if v := strings.TrimSpace(executorOverrides[id]); v != "" {
			executor = v
		}
		roles := make([]string, 0, len(cfg.Artifacts))
		for _, a := range cfg.Artifacts {
			roles = append(roles, strings.TrimSpace(a.Role))
		}
		rules := make([]ArtifactRule, 0, len(cfg.Artifacts))
		for _, a := range cfg.Artifacts {
			rules = append(rules, ArtifactRule{
				Role: strings.TrimSpace(a.Role), Kind: strings.TrimSpace(a.Kind), Glob: strings.TrimSpace(a.Glob),
				ContentType: strings.TrimSpace(a.ContentType), Required: true, Signed: a.Signed,
			})
		}
		out = append(out, TargetPlan{
			ID: id, OS: osName, Arch: arch, ExecutorID: executor, Required: true,
			BuildCommand: strings.TrimSpace(cfg.Build), SignCommand: strings.TrimSpace(cfg.Sign),
			VerifyCommand: strings.TrimSpace(cfg.Verify),
			ArtifactRoles: roles, Artifacts: rules,
		})
	}
	return out, nil
}

func splitTargetID(id string) (string, string) {
	parts := strings.SplitN(id, "-", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return strings.ToLower(parts[0]), strings.ToLower(parts[1])
}
