package store

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeTextDropsNULBytes(t *testing.T) {
	if got := sanitizeText("a\x00b\x00"); got != "ab" {
		t.Fatalf("sanitizeText = %q", got)
	}
	if got := sanitizeText("plain ascii"); got != "plain ascii" {
		t.Fatalf("sanitizeText = %q", got)
	}
	broken := sanitizeText("ok\xffbad")
	if !utf8.ValidString(broken) || !strings.Contains(broken, "ok") {
		t.Fatalf("invalid UTF-8 must be replaced, got %q", broken)
	}
}

func TestTruncateCommandOutputCutsOnRuneBoundary(t *testing.T) {
	value := strings.Repeat("中", maxExecutorCommandOutput/3+10)
	out := truncateCommandOutput(value)
	if !utf8.ValidString(out) {
		t.Fatal("truncated output must stay valid UTF-8")
	}
	if len(out) > maxExecutorCommandOutput+64 {
		t.Fatalf("output too long: %d", len(out))
	}
}
