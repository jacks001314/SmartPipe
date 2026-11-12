package releasev2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// BundleSources mirrors the upgrade service distribution sources block.
type BundleSources struct {
	Primary string   `json:"primary"`
	Mirrors []string `json:"mirrors,omitempty"`
}

// BundleLayout maps release artifacts to upgrade-service slots. Product slots
// are keyed by slot name (desktop, app-server, ...) and each maps to the
// artifact role produced by the executor. Non product components (templates,
// skills) can be attached with Components: artifact role -> component name.
type BundleLayout struct {
	Product           string            `json:"product"`
	Slots             map[string]string `json:"slots"`
	Components        map[string]string `json:"components,omitempty"`
	ComponentVersions map[string]string `json:"component_versions,omitempty"`
	Sources           BundleSources     `json:"sources"`
}

type BundleSlot struct {
	Slot        string `json:"slot"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	ArtifactID  int64  `json:"artifactId"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"contentType,omitempty"`
}

type BundleArtifact struct {
	ArtifactID  int64  `json:"artifactId"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"contentType,omitempty"`
}

type BundleComponent struct {
	Version   string           `json:"version"`
	Slots     []BundleSlot     `json:"slots,omitempty"`
	Artifacts []BundleArtifact `json:"artifacts,omitempty"`
}

type BundleMetadata struct {
	RunID         string `json:"runId"`
	ProfileSHA256 string `json:"profileSha256,omitempty"`
	CreatedBy     string `json:"createdBy"`
	CreatedAt     string `json:"createdAt"`
}

type UpgradeBundle struct {
	SchemaVersion         int                        `json:"schemaVersion"`
	ProductVersion        string                     `json:"productVersion"`
	BuildVersion          string                     `json:"buildVersion"`
	Channel               string                     `json:"channel"`
	Commit                string                     `json:"commit"`
	ReleaseManifestSHA256 string                     `json:"releaseManifestSha256"`
	Components            map[string]BundleComponent `json:"components"`
	Sources               BundleSources              `json:"sources"`
	Metadata              BundleMetadata             `json:"metadata"`
}

// BuildUpgradeBundle produces the canonical upgrade-bundle.json for a run.
// It refuses to build when a required slot has no verified artifact, when a
// digest is malformed, or when two artifacts claim the same slot cell.
func BuildUpgradeBundle(run *Run, targets []Target, artifacts []Artifact, layout BundleLayout, now time.Time) (*UpgradeBundle, []byte, string, error) {
	if run == nil {
		return nil, nil, "", fmt.Errorf("%w: run is required", ErrInvalid)
	}
	if len(layout.Slots) == 0 {
		return nil, nil, "", fmt.Errorf("%w: bundle layout has no product slots", ErrInvalid)
	}
	if strings.TrimSpace(layout.Product) == "" {
		layout.Product = "product"
	}
	if len(layout.Sources.Mirrors) > 0 {
		sort.Strings(layout.Sources.Mirrors)
	}

	targetByID := map[string]Target{}
	for _, t := range targets {
		targetByID[t.TargetID] = t
	}
	type cell struct {
		os, arch, slot string
	}
	seen := map[cell]string{}
	slots := []BundleSlot{}
	artifactsByID := map[int64]Artifact{}
	componentExtras := map[string][]BundleArtifact{}

	for _, a := range artifacts {
		artifactsByID[a.ID] = a
	}
	// Deterministic iteration over slot rules.
	slotNames := make([]string, 0, len(layout.Slots))
	for slot := range layout.Slots {
		slotNames = append(slotNames, slot)
	}
	sort.Strings(slotNames)
	for _, slot := range slotNames {
		role := strings.TrimSpace(layout.Slots[slot])
		if role == "" {
			role = slot
		}
		matched := 0
		for _, a := range artifacts {
			if a.Role != role {
				continue
			}
			t, ok := targetByID[a.TargetID]
			if !ok {
				return nil, nil, "", fmt.Errorf("%w: artifact %d references unknown target %s", ErrInvalid, a.ID, a.TargetID)
			}
			if !validDigest(a.SHA256) {
				return nil, nil, "", fmt.Errorf("%w: artifact %s has invalid sha256", ErrInvalid, a.Name)
			}
			if a.Size <= 0 {
				return nil, nil, "", fmt.Errorf("%w: artifact %s has no size", ErrInvalid, a.Name)
			}
			key := cell{os: t.OS, arch: t.Arch, slot: slot}
			if prev, dup := seen[key]; dup {
				return nil, nil, "", fmt.Errorf("%w: slot %s/%s/%s already provided by %s", ErrInvalid, slot, t.OS, t.Arch, prev)
			}
			seen[key] = a.Name
			matched++
			slots = append(slots, BundleSlot{
				Slot: slot, OS: t.OS, Arch: t.Arch, ArtifactID: a.ID, Name: a.Name,
				Size: a.Size, SHA256: a.SHA256, ContentType: a.ContentType,
			})
		}
		if matched == 0 {
			return nil, nil, "", fmt.Errorf("%w: slot %s has no artifacts for role %s", ErrInvalid, slot, role)
		}
	}
	// Required targets must contribute at least one product slot.
	for _, t := range targets {
		if !t.Required {
			continue
		}
		has := false
		for _, s := range slots {
			if s.OS == t.OS && s.Arch == t.Arch {
				has = true
				break
			}
		}
		if !has {
			return nil, nil, "", fmt.Errorf("%w: required target %s has no product slot artifact", ErrInvalid, t.TargetID)
		}
	}
	sort.Slice(slots, func(i, j int) bool {
		if slots[i].Slot != slots[j].Slot {
			return slots[i].Slot < slots[j].Slot
		}
		if slots[i].OS != slots[j].OS {
			return slots[i].OS < slots[j].OS
		}
		return slots[i].Arch < slots[j].Arch
	})

	// Non product components.
	roleNames := make([]string, 0, len(layout.Components))
	for role := range layout.Components {
		roleNames = append(roleNames, role)
	}
	sort.Strings(roleNames)
	for _, role := range roleNames {
		component := strings.TrimSpace(layout.Components[role])
		if component == "" {
			continue
		}
		for _, a := range artifacts {
			if a.Role != role {
				continue
			}
			if !validDigest(a.SHA256) || a.Size <= 0 {
				return nil, nil, "", fmt.Errorf("%w: artifact %s has an invalid digest or size", ErrInvalid, a.Name)
			}
			componentExtras[component] = append(componentExtras[component], BundleArtifact{
				ArtifactID: a.ID, Name: a.Name, Size: a.Size, SHA256: a.SHA256, ContentType: a.ContentType,
			})
		}
	}

	manifestView := struct {
		RunID     string     `json:"runId"`
		Version   string     `json:"version"`
		Commit    string     `json:"commit"`
		Targets   []Target   `json:"targets"`
		Artifacts []Artifact `json:"artifacts"`
	}{RunID: run.ID, Version: run.Version, Commit: run.ResolvedCommit, Targets: targets, Artifacts: artifacts}
	manifestSHA, err := HashJSON(manifestView)
	if err != nil {
		return nil, nil, "", err
	}
	components := map[string]BundleComponent{
		layout.Product: {Version: run.Version, Slots: slots},
	}
	for name, extra := range componentExtras {
		sort.Slice(extra, func(i, j int) bool { return extra[i].Name < extra[j].Name })
		components[name] = BundleComponent{Version: layout.ComponentVersions[name], Artifacts: extra}
	}
	for name, version := range layout.ComponentVersions {
		if _, ok := components[name]; !ok {
			return nil, nil, "", fmt.Errorf("%w: component %s has a version but no artifacts", ErrInvalid, name)
		}
		if strings.TrimSpace(version) == "" {
			return nil, nil, "", fmt.Errorf("%w: component %s has an empty version", ErrInvalid, name)
		}
	}

	bundle := &UpgradeBundle{
		SchemaVersion: 1, ProductVersion: run.Version, BuildVersion: run.BuildVersion,
		Channel: run.Channel, Commit: run.ResolvedCommit, ReleaseManifestSHA256: manifestSHA,
		Components: components, Sources: layout.Sources,
		Metadata: BundleMetadata{
			RunID: run.ID, ProfileSHA256: run.ProfileSHA256, CreatedBy: "release-service",
			CreatedAt: now.UTC().Format(time.RFC3339),
		},
	}
	raw, err := canonicalJSON(bundle)
	if err != nil {
		return nil, nil, "", err
	}
	return bundle, raw, HashText(string(raw)), nil
}

func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	out := bytes.TrimRight(buf.Bytes(), "\n")
	return out, nil
}

func validDigest(value string) bool {
	v := strings.TrimSpace(value)
	if strings.HasPrefix(v, "sha256:") {
		v = strings.TrimPrefix(v, "sha256:")
	}
	if len(v) != 64 {
		return false
	}
	for _, r := range v {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
