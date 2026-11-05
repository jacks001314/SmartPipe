package mcp

import "testing"

func TestLeadOnlyRepoTools(t *testing.T) {
	for _, tool := range []string{"repo.commit", "repo.push"} {
		found := false
		for _, td := range Tools {
			if td.Name != tool {
				continue
			}
			found = true
			if len(td.Roles) != 1 || td.Roles[0] != "lead" {
				t.Fatalf("%s roles = %v, want [lead]", tool, td.Roles)
			}
		}
		if !found {
			t.Fatalf("tool %s not registered", tool)
		}
		if ToolAllowed("dev", tool) || ToolAllowed("test", tool) || ToolAllowed("release", tool) {
			t.Fatalf("%s must not be allowed for dev/test/release", tool)
		}
		if !ToolAllowed("lead", tool) {
			t.Fatalf("%s must be allowed for lead", tool)
		}
	}
}
