package releasev2

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// BuildRecipe 是某个平台的构建配方。项目级配置只保存一次，发布时不再让用户输入。
type BuildRecipe struct {
	Build        string         `json:"build" yaml:"build"`
	Sign         string         `json:"sign,omitempty" yaml:"sign,omitempty"`
	Verify       string         `json:"verify,omitempty" yaml:"verify,omitempty"`
	Timeout      string         `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	ManifestPath string         `json:"manifest_path,omitempty" yaml:"manifest_path,omitempty"`
	Artifacts    []ArtifactRule `json:"artifacts,omitempty" yaml:"artifacts,omitempty"`
	// HostOS / HostArch 是实际执行构建的机器平台，为空表示与产物平台相同。
	// Syntropy 这类项目会在一台 macOS 上同时产出 Windows 与 macOS 安装包，
	// 产物平台（target id）和构建机平台必须分开表达。
	HostOS   string `json:"host_os,omitempty" yaml:"host_os,omitempty"`
	HostArch string `json:"host_arch,omitempty" yaml:"host_arch,omitempty"`
}

// ProjectReleaseConfig 是项目级发版配置。发布入口只需要项目、升级服务和构建机，
// 版本、构建命令、说明路径、渠道顺序都从这里派生。
type ProjectReleaseConfig struct {
	ProjectID            string                    `json:"project_id"`
	Builds               map[string]BuildRecipe    `json:"builds"`
	ManifestPath         string                    `json:"manifest_path,omitempty"`
	VersionFile          string                    `json:"version_file,omitempty"`
	VersionJSONPath      string                    `json:"version_json_path,omitempty"`
	BuildVersionTemplate string                    `json:"build_version_template,omitempty"`
	NotesPath            string                    `json:"notes_path,omitempty"`
	ChannelOrder         []string                  `json:"channel_order,omitempty"`
	UpdateServerIDs      []string                  `json:"update_server_ids,omitempty"`
	Publishing           map[string]map[string]any `json:"publishing,omitempty"`
	Policies             map[string]any            `json:"policies,omitempty"`
	UpdatedAt            time.Time                 `json:"updated_at,omitempty"`
}

// Normalize 补齐默认值并校验构建配方。它允许配置里暂时没有构建项，
// 这样项目可以先保存配置、再补构建命令。
func (c *ProjectReleaseConfig) Normalize() error {
	if c == nil {
		return fmt.Errorf("%w: project release config is nil", ErrInvalid)
	}
	c.ProjectID = strings.TrimSpace(c.ProjectID)
	if c.ProjectID == "" {
		return fmt.Errorf("%w: project_id is required", ErrInvalid)
	}
	if c.Builds == nil {
		c.Builds = map[string]BuildRecipe{}
	}
	cleaned := make(map[string]BuildRecipe, len(c.Builds))
	for id, recipe := range c.Builds {
		id = strings.ToLower(strings.TrimSpace(id))
		if !targetPattern.MatchString(id) {
			return fmt.Errorf("%w: invalid build target %q", ErrInvalid, id)
		}
		recipe.Build = strings.TrimSpace(recipe.Build)
		recipe.Sign = strings.TrimSpace(recipe.Sign)
		recipe.Verify = strings.TrimSpace(recipe.Verify)
		recipe.Timeout = strings.TrimSpace(recipe.Timeout)
		recipe.ManifestPath = strings.TrimSpace(recipe.ManifestPath)
		recipe.HostOS = strings.ToLower(strings.TrimSpace(recipe.HostOS))
		recipe.HostArch = strings.ToLower(strings.TrimSpace(recipe.HostArch))
		if recipe.Build == "" {
			return fmt.Errorf("%w: build target %s has no build command", ErrInvalid, id)
		}
		if (recipe.HostOS == "") != (recipe.HostArch == "") {
			return fmt.Errorf("%w: build target %s host_os and host_arch must be set together", ErrInvalid, id)
		}
		if recipe.HostOS != "" && !targetPattern.MatchString(recipe.HostOS+"-"+recipe.HostArch) {
			return fmt.Errorf("%w: build target %s has invalid host %s/%s", ErrInvalid, id, recipe.HostOS, recipe.HostArch)
		}
		if recipe.Timeout != "" {
			if _, err := time.ParseDuration(recipe.Timeout); err != nil {
				return fmt.Errorf("%w: build target %s timeout: %v", ErrInvalid, id, err)
			}
		}
		for i := range recipe.Artifacts {
			rule := &recipe.Artifacts[i]
			rule.Role = strings.TrimSpace(rule.Role)
			rule.Kind = strings.TrimSpace(rule.Kind)
			rule.Glob = strings.TrimSpace(rule.Glob)
			rule.ContentType = strings.TrimSpace(rule.ContentType)
			rule.Destinations = normalizeStrings(rule.Destinations)
			if rule.Role == "" {
				return fmt.Errorf("%w: build target %s has an artifact without role", ErrInvalid, id)
			}
			if rule.Glob == "" && recipe.ManifestPath == "" && strings.TrimSpace(c.ManifestPath) == "" {
				return fmt.Errorf("%w: build target %s artifact %s has no glob", ErrInvalid, id, rule.Role)
			}
		}
		cleaned[id] = recipe
	}
	c.Builds = cleaned
	c.ManifestPath = strings.TrimSpace(c.ManifestPath)
	c.VersionFile = strings.TrimSpace(c.VersionFile)
	c.VersionJSONPath = strings.TrimSpace(c.VersionJSONPath)
	if c.VersionJSONPath == "" {
		c.VersionJSONPath = "version"
	}
	c.BuildVersionTemplate = strings.TrimSpace(c.BuildVersionTemplate)
	if c.BuildVersionTemplate == "" {
		c.BuildVersionTemplate = "{version}"
	}
	c.NotesPath = strings.TrimSpace(c.NotesPath)
	if len(c.ChannelOrder) == 0 {
		c.ChannelOrder = []string{"beta", "stable"}
	}
	seen := map[string]bool{}
	channels := make([]string, 0, len(c.ChannelOrder))
	for _, channel := range c.ChannelOrder {
		channel = strings.TrimSpace(channel)
		if channel == "" || seen[channel] {
			continue
		}
		seen[channel] = true
		channels = append(channels, channel)
	}
	if len(channels) == 0 {
		return fmt.Errorf("%w: channel_order is empty", ErrInvalid)
	}
	c.ChannelOrder = channels
	c.UpdateServerIDs = normalizeStrings(c.UpdateServerIDs)
	return nil
}

// TargetIDs 返回配置里的平台，按稳定顺序排序。
func (c *ProjectReleaseConfig) TargetIDs() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.Builds))
	for id := range c.Builds {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ManifestPathFor 返回平台级清单路径，未配置时回落到项目级路径。
func (c *ProjectReleaseConfig) ManifestPathFor(targetID string) string {
	if c == nil {
		return ""
	}
	if recipe, ok := c.Builds[targetID]; ok && strings.TrimSpace(recipe.ManifestPath) != "" {
		return strings.TrimSpace(recipe.ManifestPath)
	}
	return strings.TrimSpace(c.ManifestPath)
}

func normalizeStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
