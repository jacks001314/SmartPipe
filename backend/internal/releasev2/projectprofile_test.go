package releasev2

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestBundledProjectProfilesProduceCompleteBundles 校验仓库里随附的项目配置：
// 一、能被 ProjectReleaseConfig 解析；二、按配置声明的制品规则，一次发布能
// 覆盖升级服务的全部产品槽位与内容组件。Syntropy 的 product 组件有 27 个
// 槽位，少一个槽位就会把线上渠道指针里的成员抹掉。
func TestBundledProjectProfilesProduceCompleteBundles(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "..", "deploy", "profiles", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Skip("no bundled project profiles")
	}
	for _, path := range paths {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var cfg ProjectReleaseConfig
			if err := json.Unmarshal(raw, &cfg); err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}
			// 项目 ID 是环境数据，profile 文件里不写死。
			cfg.ProjectID = "prj_profile_test"
			if err := cfg.Normalize(); err != nil {
				t.Fatalf("normalize %s: %v", path, err)
			}
			bundle := bundleFromProfile(t, &cfg)
			want := 0
			slotRoles := map[string]bool{}
			for _, role := range profileSlots(t, &cfg) {
				slotRoles[role] = true
			}
			for _, recipe := range cfg.Builds {
				for _, rule := range recipe.Artifacts {
					if slotRoles[rule.Role] {
						want++
					}
				}
			}
			if got := len(bundle.Components["product"].Slots); got != want {
				t.Fatalf("product slots = %d, want %d (%+v)", got, want, bundle.Components["product"].Slots)
			}
		})
	}
}

func profileSlots(t *testing.T, cfg *ProjectReleaseConfig) map[string]string {
	t.Helper()
	out := map[string]string{}
	cfgPublishing := cfg.Publishing["update_service"]
	slots, _ := cfgPublishing["productSlots"].(map[string]any)
	for name, rawEntry := range slots {
		entry, _ := rawEntry.(map[string]any)
		role, _ := entry["artifactRole"].(string)
		if role == "" {
			role = name
		}
		out[name] = role
	}
	if len(out) == 0 {
		t.Fatal("profile declares no product slots")
	}
	return out
}

// bundleFromProfile 把配置里的制品规则展开成一次完整发布的工件清单：
// 每个 build target 按规则产出一份制品，交叉编译靠规则上的 os/arch 覆盖。
func bundleFromProfile(t *testing.T, cfg *ProjectReleaseConfig) *UpgradeBundle {
	t.Helper()
	cfgPublishing := cfg.Publishing["update_service"]
	components := map[string]string{}
	if raw, ok := cfgPublishing["components"].(map[string]any); ok {
		for name, rawEntry := range raw {
			entry, _ := rawEntry.(map[string]any)
			if role, _ := entry["artifactRole"].(string); role != "" {
				components[role] = name
			}
		}
	}
	layout := BundleLayout{Product: "product", Slots: profileSlots(t, cfg), Components: components}

	run := &Run{
		ID: "rrun_profile", Version: "1.2.3", BuildVersion: "1.2.3.2026-01-01", Channel: "stable",
		ResolvedCommit: "0123456789abcdef0123456789abcdef01234567", Status: RunStaged,
	}
	targetIDs := make([]string, 0, len(cfg.Builds))
	for id := range cfg.Builds {
		targetIDs = append(targetIDs, id)
	}
	sort.Strings(targetIDs)
	var targets []Target
	var artifacts []Artifact
	var id int64
	for _, targetID := range targetIDs {
		recipe := cfg.Builds[targetID]
		osName, archName := splitTargetID(targetID)
		targets = append(targets, Target{TargetID: targetID, OS: osName, Arch: archName, Required: true, Status: TargetReady})
		for _, rule := range recipe.Artifacts {
			osForArtifact := strings.ToLower(firstNonEmptyString(rule.OS, osName))
			archForArtifact := strings.ToLower(firstNonEmptyString(rule.Arch, archName))
			name := rule.Role + "-" + osForArtifact + "-" + archForArtifact + ".bin"
			if component, ok := components[rule.Role]; ok {
				name = component + "-1.0.0.tar.gz"
			}
			id++
			artifacts = append(artifacts, Artifact{
				ID: id, RunID: run.ID, TargetID: targetID, Role: rule.Role,
				Name: name, OS: rule.OS, Arch: rule.Arch,
				Size: 1024, SHA256: "sha256:" + strings.Repeat("a", 64),
			})
		}
	}
	bundle, _, _, err := BuildUpgradeBundle(run, targets, artifacts, layout, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build bundle: %v", err)
	}
	for _, name := range components {
		if component, ok := bundle.Components[name]; !ok || component.Version != "1.0.0" {
			t.Fatalf("content component %s = %+v", name, component)
		}
	}
	return bundle
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
