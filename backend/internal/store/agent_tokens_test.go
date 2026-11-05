package store

import (
	"context"
	"testing"
	"time"
)

func TestAgentTokenExpiry(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, _ = s.DB().Exec("DELETE FROM agent_tokens WHERE name LIKE 'expiry-test-%'")

	soon := time.Now().UTC().Add(time.Hour)
	tok, raw, err := s.CreateAgentToken(ctx, AgentToken{Role: "lead", Name: "expiry-test-live", ExpiresAt: &soon})
	if err != nil {
		t.Fatalf("create live: %v", err)
	}
	if tok.ExpiresAt == nil || tok.ExpiresAt.Before(time.Now()) {
		t.Fatalf("expires_at not stored: %+v", tok)
	}
	if _, err := s.VerifyAgentToken(ctx, raw); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}

	past := time.Now().UTC().Add(-time.Minute)
	_, expiredRaw, err := s.CreateAgentToken(ctx, AgentToken{Role: "lead", Name: "expiry-test-past", ExpiresAt: &past})
	if err != nil {
		t.Fatalf("create expired: %v", err)
	}
	if _, err := s.VerifyAgentToken(ctx, expiredRaw); err != ErrNotFound {
		t.Fatalf("expired token verify = %v, want ErrNotFound", err)
	}

	perm, permRaw, err := s.CreateAgentToken(ctx, AgentToken{Role: "dev", Name: "expiry-test-perm"})
	if err != nil {
		t.Fatalf("create perm: %v", err)
	}
	if perm.ExpiresAt != nil {
		t.Fatalf("permanent token should have nil expires_at: %+v", perm)
	}
	if _, err := s.VerifyAgentToken(ctx, permRaw); err != nil {
		t.Fatalf("permanent token rejected: %v", err)
	}

	list, err := s.ListAgentTokens(ctx, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var seen int
	for _, item := range list {
		if item.Name == "expiry-test-live" && item.ExpiresAt != nil {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("expires_at missing in list: %d", seen)
	}
	_, _ = s.DB().Exec("DELETE FROM agent_tokens WHERE name LIKE 'expiry-test-%'")
}
