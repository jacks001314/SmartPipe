package adminapi

import "testing"

func TestParseGitProgress(t *testing.T) {
	cases := []struct {
		line  string
		phase string
		pct   int
		ok    bool
	}{
		{"Receiving objects:  43% (1234/5678), 12.34 MiB | 1.2 MiB/s", "Receiving objects", 43, true},
		{"remote: Counting objects: 100% (12/12), done.", "Counting objects", 100, true},
		{"Resolving deltas:   7% (10/140)", "Resolving deltas", 7, true},
		{"Cloning into 'x'...", "", 0, false},
	}
	for _, tc := range cases {
		phase, pct, ok := parseGitProgress(tc.line)
		if ok != tc.ok || phase != tc.phase || pct != tc.pct {
			t.Fatalf("parse %q = (%q,%d,%v), want (%q,%d,%v)", tc.line, phase, pct, ok, tc.phase, tc.pct, tc.ok)
		}
	}
}

func TestOverallCloneProgress(t *testing.T) {
	if got := overallCloneProgress("Receiving objects", 50); got != 45 {
		t.Fatalf("receiving 50%% = %d, want 45", got)
	}
	if got := overallCloneProgress("Resolving deltas", 100); got != 95 {
		t.Fatalf("resolving 100%% = %d, want 95", got)
	}
	if got := overallCloneProgress("Updating files", 100); got != 100 {
		t.Fatalf("updating 100%% = %d, want 100", got)
	}
}
