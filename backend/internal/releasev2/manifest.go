package releasev2

import (
	"fmt"
	"path"
	"strings"
	"time"
)

// ArtifactManifestSchema is the version of the executor manifest contract.
const ArtifactManifestSchema = 1

// ArtifactManifest is produced by the build executor after every artifact has
// been uploaded. The service treats it as the authoritative list of files that
// belong to one target; glob rules are only used to discover candidates.
type ArtifactManifest struct {
	Schema       int                `json:"schema"`
	RunID        string             `json:"run_id"`
	TargetID     string             `json:"target_id"`
	OS           string             `json:"os"`
	Arch         string             `json:"arch"`
	Version      string             `json:"version"`
	BuildVersion string             `json:"build_version"`
	Commit       string             `json:"commit"`
	GeneratedAt  time.Time          `json:"generated_at"`
	Artifacts    []ManifestArtifact `json:"artifacts"`
}

type ManifestArtifact struct {
	Role string `json:"role"`
	Kind string `json:"kind,omitempty"`
	Path string `json:"path"`
	Name string `json:"name"`
	// OS and Arch, when set, override the target platform for this file. One
	// build host may cross-build artifacts for another platform.
	OS           string   `json:"os,omitempty"`
	Arch         string   `json:"arch,omitempty"`
	Size         int64    `json:"size"`
	SHA256       string   `json:"sha256"`
	ContentType  string   `json:"content_type,omitempty"`
	Required     bool     `json:"required"`
	Destinations []string `json:"destinations,omitempty"`
}

// RunArtifactManifest is the merged, run-level view used by publishing.
type RunArtifactManifest struct {
	Schema       int                `json:"schema"`
	RunID        string             `json:"run_id"`
	Version      string             `json:"version"`
	BuildVersion string             `json:"build_version"`
	Commit       string             `json:"commit"`
	Targets      []ArtifactManifest `json:"targets"`
}

// DefaultDestinations returns the delivery destinations for an artifact. An
// explicit rule wins; otherwise the artifact kind provides a safe default.
func DefaultDestinations(kind string, explicit []string) []string {
	if len(explicit) > 0 {
		out := make([]string, 0, len(explicit))
		seen := map[string]bool{}
		for _, item := range explicit {
			item = strings.TrimSpace(item)
			if item == "" || seen[item] {
				continue
			}
			seen[item] = true
			out = append(out, item)
		}
		if len(out) > 0 {
			return out
		}
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "thin", "update-thin":
		return []string{"update:thin"}
	case "installer", "full", "update-full":
		return []string{"github", "update:full"}
	case "manifest":
		return []string{"update:manifest"}
	default:
		return []string{"github"}
	}
}

func (m *ArtifactManifest) Normalize() {
	if m == nil {
		return
	}
	if m.Schema == 0 {
		m.Schema = ArtifactManifestSchema
	}
	m.RunID = strings.TrimSpace(m.RunID)
	m.TargetID = strings.TrimSpace(m.TargetID)
	m.OS = strings.ToLower(strings.TrimSpace(m.OS))
	m.Arch = strings.ToLower(strings.TrimSpace(m.Arch))
	for i := range m.Artifacts {
		item := &m.Artifacts[i]
		item.Role = strings.TrimSpace(item.Role)
		item.Kind = strings.TrimSpace(item.Kind)
		item.Path = strings.TrimSpace(item.Path)
		item.Name = strings.TrimSpace(item.Name)
		item.OS = strings.ToLower(strings.TrimSpace(item.OS))
		item.Arch = strings.ToLower(strings.TrimSpace(item.Arch))
		item.SHA256 = strings.TrimSpace(item.SHA256)
		item.ContentType = strings.TrimSpace(item.ContentType)
		if item.Path != "" {
			item.Path = path.Clean(strings.ReplaceAll(item.Path, "\\", "/"))
		}
		item.Destinations = DefaultDestinations(item.Kind, item.Destinations)
	}
}

// Validate checks the manifest before it is persisted or used for publishing.
func (m *ArtifactManifest) Validate() error {
	if m == nil {
		return fmt.Errorf("%w: artifact manifest is nil", ErrInvalid)
	}
	m.Normalize()
	if m.Schema != ArtifactManifestSchema {
		return fmt.Errorf("%w: unsupported artifact manifest schema %d", ErrInvalid, m.Schema)
	}
	if m.RunID == "" || m.TargetID == "" || m.OS == "" || m.Arch == "" {
		return fmt.Errorf("%w: manifest run_id, target_id, os and arch are required", ErrInvalid)
	}
	seen := map[string]bool{}
	for i, item := range m.Artifacts {
		if item.Role == "" || item.Name == "" || item.Path == "" {
			return fmt.Errorf("%w: manifest artifact %d lacks role, name or path", ErrInvalid, i)
		}
		if item.Size <= 0 {
			return fmt.Errorf("%w: manifest artifact %s has no size", ErrInvalid, item.Name)
		}
		if !validDigest(item.SHA256) {
			return fmt.Errorf("%w: manifest artifact %s has invalid sha256", ErrInvalid, item.Name)
		}
		if path.IsAbs(item.Path) || item.Path == ".." || strings.HasPrefix(item.Path, "../") {
			return fmt.Errorf("%w: manifest artifact %s has unsafe path %q", ErrInvalid, item.Name, item.Path)
		}
		key := item.Role + "\x00" + item.Name
		if seen[key] {
			return fmt.Errorf("%w: duplicate manifest artifact %s/%s", ErrInvalid, item.Role, item.Name)
		}
		seen[key] = true
	}
	return nil
}

// MergeArtifactManifests creates one deterministic run-level manifest.
func MergeArtifactManifests(run *Run, manifests []ArtifactManifest) (*RunArtifactManifest, error) {
	if run == nil {
		return nil, fmt.Errorf("%w: run is required", ErrInvalid)
	}
	out := &RunArtifactManifest{
		Schema: ArtifactManifestSchema, RunID: run.ID, Version: run.Version,
		BuildVersion: run.BuildVersion, Commit: run.ResolvedCommit,
		Targets: append([]ArtifactManifest(nil), manifests...),
	}
	seen := map[string]bool{}
	for i := range out.Targets {
		item := &out.Targets[i]
		if err := item.Validate(); err != nil {
			return nil, err
		}
		if item.RunID != run.ID {
			return nil, fmt.Errorf("%w: manifest run %s does not match %s", ErrInvalid, item.RunID, run.ID)
		}
		if seen[item.TargetID] {
			return nil, fmt.Errorf("%w: duplicate target manifest %s", ErrInvalid, item.TargetID)
		}
		seen[item.TargetID] = true
	}
	return out, nil
}
