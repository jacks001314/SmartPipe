package adminapi

import (
	"testing"

	"smartpipe/backend/internal/releasev2"
)

func TestAttentionEligibleCoversEveryRecoverableState(t *testing.T) {
	cases := []struct {
		name string
		run  releasev2.Run
		want bool
	}{
		{name: "needs_attention build_only", run: releasev2.Run{Status: releasev2.RunNeedsAttention, Mode: releasev2.ModeBuildOnly}, want: true},
		{name: "needs_attention publish", run: releasev2.Run{Status: releasev2.RunNeedsAttention, Mode: releasev2.ModePublish}, want: true},
		{name: "partially_published", run: releasev2.Run{Status: releasev2.RunPartiallyPublished, Mode: releasev2.ModePublish}, want: true},
		{name: "staged", run: releasev2.Run{Status: releasev2.RunStaged, Mode: releasev2.ModePublish}, want: true},
		{name: "publishing", run: releasev2.Run{Status: releasev2.RunPublishing, Mode: releasev2.ModePublish}, want: true},
		{name: "artifacts_ready publish", run: releasev2.Run{Status: releasev2.RunArtifactsReady, Mode: releasev2.ModePublish}, want: true},
		{name: "artifacts_ready build_only", run: releasev2.Run{Status: releasev2.RunArtifactsReady, Mode: releasev2.ModeBuildOnly}, want: false},
		{name: "built", run: releasev2.Run{Status: releasev2.RunBuilt, Mode: releasev2.ModeBuildOnly}, want: false},
		{name: "published", run: releasev2.Run{Status: releasev2.RunPublished, Mode: releasev2.ModePublish}, want: false},
		{name: "failed", run: releasev2.Run{Status: releasev2.RunFailed, Mode: releasev2.ModePublish}, want: false},
		{name: "cancelled", run: releasev2.Run{Status: releasev2.RunCancelled, Mode: releasev2.ModePublish}, want: false},
		{name: "nil run", run: releasev2.Run{}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := attentionEligible(&tc.run); got != tc.want {
				t.Fatalf("attentionEligible(%s/%s) = %v, want %v", tc.run.Status, tc.run.Mode, got, tc.want)
			}
		})
	}
}

func TestAttentionEligibleIgnoresNilRun(t *testing.T) {
	if attentionEligible(nil) {
		t.Fatal("nil run must not be eligible")
	}
}
