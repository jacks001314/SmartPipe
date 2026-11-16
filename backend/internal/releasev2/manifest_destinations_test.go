package releasev2

import (
	"strings"
	"testing"
)

func TestDefaultDestinations(t *testing.T) {
	cases := map[string]string{
		"installer": "github,update:full",
		"thin":      "update:thin",
		"manifest":  "update:manifest",
		"binary":    "update:full",
		"content":   "update:full",
	}
	for kind, want := range cases {
		if got := strings.Join(DefaultDestinations(kind, nil), ","); got != want {
			t.Fatalf("kind %s = %q, want %q", kind, got, want)
		}
	}
	explicit := DefaultDestinations("binary", []string{"github"})
	if len(explicit) != 1 || explicit[0] != "github" {
		t.Fatalf("explicit destinations = %v", explicit)
	}
}
