package patch

import "testing"

func TestParseRefSHAIgnoresWarnings(t *testing.T) {
	out := "warning: redirecting to https://github.com/example/repo.git/\n" +
		"e89c891dd5132fffba1db2822a65c32cd6a5cee4\trefs/heads/agent/issue-7\n"
	if got := parseRefSHA(out); got != "e89c891dd5132fffba1db2822a65c32cd6a5cee4" {
		t.Fatalf("unexpected sha: %q", got)
	}
	if got := parseRefSHA("warning: only\n"); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}
