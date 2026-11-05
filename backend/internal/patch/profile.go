package patch

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Profile 是仓库根目录 smartpipe.yaml 的验证契约。
type Profile struct {
	Version          int               `yaml:"version" json:"version"`
	Toolchain        map[string]string `yaml:"toolchain" json:"toolchain"`
	Commands         map[string]string `yaml:"commands" json:"commands"`
	RequiredForMerge []string          `yaml:"required_for_merge" json:"required_for_merge"`
	Timeouts         map[string]string `yaml:"timeouts" json:"timeouts"`
}

// DefaultTimeout 是未声明超时时的单条命令上限。
const DefaultTimeout = 10 * time.Minute

// ErrInvalidProfile 表示 smartpipe.yaml 不完整或不可用。
var ErrInvalidProfile = errors.New("patch: invalid profile")

// ParseProfile 解析 YAML 文本并做基本校验。
func ParseProfile(raw []byte) (*Profile, error) {
	var p Profile
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidProfile, err)
	}
	if p.Version == 0 {
		p.Version = 1
	}
	if len(p.Commands) == 0 {
		return nil, fmt.Errorf("%w: commands is empty", ErrInvalidProfile)
	}
	for _, name := range p.RequiredForMerge {
		if strings.TrimSpace(p.Commands[name]) == "" {
			return nil, fmt.Errorf("%w: required command %q has no definition", ErrInvalidProfile, name)
		}
	}
	if len(p.RequiredForMerge) == 0 {
		names := make([]string, 0, len(p.Commands))
		for name := range p.Commands {
			names = append(names, name)
		}
		sort.Strings(names)
		p.RequiredForMerge = names
	}
	for name, v := range p.Timeouts {
		if _, err := time.ParseDuration(strings.TrimSpace(v)); err != nil {
			return nil, fmt.Errorf("%w: timeout for %q is invalid: %v", ErrInvalidProfile, name, err)
		}
	}
	return &p, nil
}

// LoadProfile 从工作区根目录读取 smartpipe.yaml。
func LoadProfile(path string) (*Profile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidProfile, err)
	}
	return ParseProfile(raw)
}

// TimeoutFor 返回某条命令的超时。
func (p *Profile) TimeoutFor(name string) time.Duration {
	if p.Timeouts != nil {
		if v := strings.TrimSpace(p.Timeouts[name]); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				return d
			}
		}
	}
	return DefaultTimeout
}

// MergeChecks 返回按声明顺序需要执行的命令。
func (p *Profile) MergeChecks() []string {
	return append([]string(nil), p.RequiredForMerge...)
}
