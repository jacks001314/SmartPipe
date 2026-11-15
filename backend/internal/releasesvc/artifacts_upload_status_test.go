package releasesvc

import (
	"context"
	"testing"

	"smartpipe/backend/internal/releasev2"
)

// 一个 target 通常有多个制品（installer、thin、manifest）。第一件上传完
// 状态会变成 uploaded，后续制品不能因此被 409 拒绝。
func TestTargetForUploadAcceptsUploaded(t *testing.T) {
	fake := &fakeControlStore{target: &releasev2.Target{
		RunID: "run_1", TargetID: "darwin-arm64", Status: releasev2.TargetUploaded,
	}}
	h := &HTTPHandler{Store: fake}
	target, err := h.targetForUpload(context.Background(), artifactBeginRequest{RunID: "run_1", TargetID: "darwin-arm64"})
	if err != nil {
		t.Fatalf("uploaded target must accept further artifacts: %v", err)
	}
	if target == nil || target.TargetID != "darwin-arm64" {
		t.Fatalf("unexpected target: %+v", target)
	}
}
