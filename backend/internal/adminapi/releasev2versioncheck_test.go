package adminapi

import (
	"strings"
	"testing"

	"smartpipe/backend/internal/releasev2"
)

func TestUpdateServicePublishingReadsProfileValues(t *testing.T) {
	cfg := &releasev2.ProjectReleaseConfig{
		ChannelOrder: []string{"beta", "stable"},
		Publishing: map[string]map[string]any{
			"update_service": {"product": "syntropy", "channel": "stable", "credential": "cdn_token"},
		},
	}
	product, channel, credential := updateServicePublishing(cfg)
	if product != "syntropy" || channel != "stable" || credential != "cdn_token" {
		t.Fatalf("got (%q,%q,%q)", product, channel, credential)
	}
}

func TestUpdateServicePublishingFallsBackToFirstChannel(t *testing.T) {
	cfg := &releasev2.ProjectReleaseConfig{ChannelOrder: []string{"beta", "stable"}}
	product, channel, credential := updateServicePublishing(cfg)
	if product != "product" {
		t.Fatalf("product = %q, want default product", product)
	}
	if channel != "beta" {
		t.Fatalf("channel = %q, want first channel_order entry", channel)
	}
	if credential != "" {
		t.Fatalf("credential = %q, want empty", credential)
	}
}

func TestVersionWarningsSkipUnknownProbe(t *testing.T) {
	if got := versionWarnings(nil, "1.0.0"); got != nil {
		t.Fatalf("nil probe must not warn, got %v", got)
	}
	probe := &publishedVersionProbe{Product: "syntropy", Channel: "beta", Checked: []string{"cdn", "app"}}
	if got := versionWarnings(probe, "1.0.0"); got != nil {
		t.Fatalf("no found_on must not warn, got %v", got)
	}
}

func TestVersionWarningsNameEveryService(t *testing.T) {
	probe := &publishedVersionProbe{
		Product: "syntropy", Channel: "beta", Checked: []string{"app", "cdn"},
		FoundOn: []string{"app", "cdn"},
	}
	warnings := versionWarnings(probe, "0.5.17")
	if len(warnings) != 1 {
		t.Fatalf("warnings = %d, want 1", len(warnings))
	}
	w := warnings[0]
	if w.Code != "version_already_published" {
		t.Fatalf("code = %q", w.Code)
	}
	for _, want := range []string{"0.5.17", "app", "cdn", "syntropy", "beta"} {
		if !strings.Contains(w.Message, want) {
			t.Fatalf("message %q missing %q", w.Message, want)
		}
	}
}
