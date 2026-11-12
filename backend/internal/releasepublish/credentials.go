// Package releasepublish contains the external adapters used by the release
// control plane. Adapters never accept secret values from the Agent: callers
// pass a credential reference that the server resolves from its own secret
// store or environment.
package releasepublish

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

var credentialRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// Provider resolves an opaque credential reference to its secret value.
// Implementations must never persist or log the returned value.
type Provider interface {
	Resolve(ctx context.Context, ref string) (string, error)
}

// EnvProvider resolves credential references from SMARTPIPE_CRED_<REF>.
// Reference characters are upper-cased and non alphanumerics become "_".
type EnvProvider struct {
	Prefix string
	Lookup func(string) (string, bool)
}

func (p EnvProvider) Resolve(_ context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("%w: credential reference is required", ErrInvalid)
	}
	if !credentialRefPattern.MatchString(ref) {
		return "", fmt.Errorf("%w: invalid credential reference", ErrInvalid)
	}
	prefix := p.Prefix
	if prefix == "" {
		prefix = "SMARTPIPE_CRED_"
	}
	name := prefix + strings.ToUpper(envSafe(ref))
	lookup := p.Lookup
	if lookup == nil {
		return "", fmt.Errorf("%w: no credential lookup configured", ErrInvalid)
	}
	value, ok := lookup(name)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%w: credential %q is not configured", ErrCredentialMissing, ref)
	}
	return value, nil
}

func envSafe(ref string) string {
	var b strings.Builder
	for _, r := range ref {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
