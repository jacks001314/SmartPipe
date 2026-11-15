package releasev2

import "testing"

func TestProjectReleaseConfigHostPlatform(t *testing.T) {
	cfg := &ProjectReleaseConfig{ProjectID: "prj_1", Builds: map[string]BuildRecipe{
		"windows-amd64": {Build: "make", HostOS: "DARWIN", HostArch: "AMD64"},
	}}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	recipe := cfg.Builds["windows-amd64"]
	if recipe.HostOS != "darwin" || recipe.HostArch != "amd64" {
		t.Fatalf("host=%q/%q", recipe.HostOS, recipe.HostArch)
	}
}

func TestProjectReleaseConfigHostPlatformMustBePaired(t *testing.T) {
	cfg := &ProjectReleaseConfig{ProjectID: "prj_1", Builds: map[string]BuildRecipe{
		"windows-amd64": {Build: "make", HostOS: "darwin"},
	}}
	if err := cfg.Normalize(); err == nil {
		t.Fatal("expected error for unpaired host platform")
	}
}
