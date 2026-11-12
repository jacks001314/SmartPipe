package releasemcp

import (
	"testing"

	"smartpipe/backend/internal/releasev2"
)

func TestToolsForScopes(t *testing.T) {
	tools := toolsFor([]string{"release.read", "artifact.read"})
	names := map[string]bool{}
	for _, tool := range tools {
		name, _ := tool["name"].(string)
		names[name] = true
	}
	if !names["release_run_get"] || !names["artifact_list"] {
		t.Fatalf("missing allowed tools: %v", names)
	}
	if names["executor_list"] {
		t.Fatalf("executor tool leaked without scope: %v", names)
	}
	if !releasev2.HasScope([]string{"release.*"}, "release.publish") {
		t.Fatal("wildcard scope did not match")
	}
}

func TestRedactArguments(t *testing.T) {
	got := redactArguments(map[string]any{"target": "darwin-arm64", "admin_token": "secret"})
	if got["target"] != "darwin-arm64" || got["admin_token"] != "[redacted]" {
		t.Fatalf("redaction = %+v", got)
	}
}

func TestWriteToolsFollowScopes(t *testing.T) {
	names := func(scopes ...string) map[string]bool {
		out := map[string]bool{}
		for _, tool := range toolsFor(scopes) {
			name, _ := tool["name"].(string)
			out[name] = true
		}
		return out
	}
	readOnly := names("release.read", "artifact.read")
	if readOnly["build_dispatch"] || readOnly["update_import_stage"] || readOnly["release_publish"] {
		t.Fatalf("write tools leaked to read-only token: %v", readOnly)
	}
	build := names("build.read")
	if !build["build_dispatch"] || !build["build_wait"] || build["build_retry"] {
		t.Fatalf("build scope tools = %v", build)
	}
	publish := names("release.publish")
	if !publish["release_publish"] || publish["update_import_stage"] {
		t.Fatalf("publish scope tools = %v", publish)
	}
	update := names("update.write")
	if !update["update_import_stage"] || !update["update_import_get"] || update["update_channel_promote"] {
		t.Fatalf("update scope tools = %v", update)
	}
	if !allowedTool([]string{"build.write"}, "build_wait") {
		t.Fatal("build.write should allow build_wait")
	}
	if allowedTool([]string{"update.write"}, "update_channel_promote") {
		t.Fatal("update.write must not allow channel promote")
	}
	gh := names("github.write")
	if !gh["github_draft_create"] || !gh["github_asset_upload"] || gh["release_publish"] {
		t.Fatalf("github scope tools = %v", gh)
	}
	if allowedTool([]string{"release.publish"}, "github_release_publish") {
		t.Fatal("release.publish must not imply github.write")
	}
}

func TestUpdateCredentialPrecedence(t *testing.T) {
	profile := &releasev2.Profile{Release: &releasev2.ReleaseConfig{
		Publishing: map[string]map[string]any{"update_service": {"credential": "from-profile"}},
	}}
	if got := updateCredentialFrom(profile, "arg-ref", "server-default"); got != "arg-ref" {
		t.Fatalf("argument should win, got %q", got)
	}
	if got := updateCredentialFrom(profile, "", "server-default"); got != "from-profile" {
		t.Fatalf("profile should beat server default, got %q", got)
	}
	if got := updateCredentialFrom(nil, "", "server-default"); got != "server-default" {
		t.Fatalf("fallback missing, got %q", got)
	}
	if got := updateCredentialFrom(nil, "", ""); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}
