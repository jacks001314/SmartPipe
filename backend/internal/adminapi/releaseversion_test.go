package adminapi

import (
	"strings"
	"testing"

	"smartpipe/backend/internal/releasev2"
)

func TestSetJSONStringKeepsOtherFields(t *testing.T) {
	raw := "{\n  \"name\": \"demo\",\n  \"version\": \"0.5.18\",\n  \"scripts\": {\"version\": \"nested\"},\n  \"files\": [\"version\"]\n}\n"
	got, err := setJSONString(raw, "version", "0.5.19")
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"name\": \"demo\",\n  \"version\": \"0.5.19\",\n  \"scripts\": {\"version\": \"nested\"},\n  \"files\": [\"version\"]\n}\n"
	if got != want {
		t.Fatalf("unexpected rewrite:\n%s", got)
	}
}

func TestSetJSONStringNestedPath(t *testing.T) {
	raw := "{\n  \"package\": {\n    \"version\": \"1.0.0\"\n  }\n}\n"
	got, err := setJSONString(raw, "package.version", "1.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "\"version\": \"1.0.1\"") || strings.Contains(got, "1.0.0") {
		t.Fatalf("nested rewrite = %s", got)
	}
}

func TestSetJSONStringMissingField(t *testing.T) {
	if _, err := setJSONString("{\"name\":\"demo\"}", "version", "1.0.0"); err == nil {
		t.Fatal("missing field accepted")
	}
}

func TestReleaseVersionFiles(t *testing.T) {
	cfg := &releasev2.ProjectReleaseConfig{VersionFile: "desktop_app/package.json, desktop_app/apps/desktop/package.json\ndesktop_app/package.json"}
	files := releaseVersionFiles(cfg)
	if len(files) != 2 || files[0] != "desktop_app/package.json" || files[1] != "desktop_app/apps/desktop/package.json" {
		t.Fatalf("files = %v", files)
	}
	if got := releaseVersionFiles(nil); len(got) != 0 {
		t.Fatalf("nil config = %v", got)
	}
}
