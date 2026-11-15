package releasemcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"smartpipe/backend/internal/releasepublish"
	"smartpipe/backend/internal/releasev2"
)

// 同版本已存在但摘要不同必须被拦下来：升级服务不允许同版本换字节，
// 客户端也只比较版本号，悄悄覆盖等于对已装用户不生效。
func TestCheckVersionConflicts(t *testing.T) {
	digestA := "sha256:" + strings.Repeat("a", 64)
	digestB := "sha256:" + strings.Repeat("b", 64)
	digestC := "sha256:" + strings.Repeat("c", 64)

	bundle := releasev2.UpgradeBundle{
		ProductVersion: "0.5.17", Channel: "beta",
		Components: map[string]releasev2.BundleComponent{
			"product": {Version: "0.5.17", Slots: []releasev2.BundleSlot{
				{Slot: "desktop", OS: "darwin", Arch: "arm64", SHA256: digestA},
				{Slot: "mbot", OS: "linux", Arch: "amd64", SHA256: digestB},
			}},
		},
	}
	layout := releasev2.BundleLayout{Product: "product"}

	serve := func(remoteDigests map[string]string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1/admin/releases":
				_ = json.NewEncoder(w).Encode(map[string]any{"releases": []map[string]any{
					{"id": "rel-1", "component": "product", "version": "0.5.17", "channel": "beta"},
				}})
			case "/v1/admin/releases/rel-1":
				artifacts := make([]map[string]any, 0, len(remoteDigests))
				for cell, digest := range remoteDigests {
					parts := strings.SplitN(cell, "/", 3)
					artifacts = append(artifacts, map[string]any{
						"slot": parts[0], "os": parts[1], "arch": parts[2], "sha256": digest,
					})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"release":   map[string]any{"id": "rel-1", "component": "product", "version": "0.5.17", "channel": "beta"},
					"artifacts": artifacts,
				})
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
	}

	cases := []struct {
		name    string
		remote  map[string]string
		wantErr bool
	}{
		{"identical digests pass", map[string]string{"desktop/darwin/arm64": digestA, "mbot/linux/amd64": digestB}, false},
		{"different digest blocks", map[string]string{"desktop/darwin/arm64": digestA, "mbot/linux/amd64": digestC}, true},
		{"missing cells are fine", map[string]string{"desktop/darwin/arm64": digestA}, false},
		{"no release", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := serve(tc.remote)
			defer srv.Close()
			if tc.remote == nil {
				srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_ = json.NewEncoder(w).Encode(map[string]any{"releases": []map[string]any{}})
				})
			}
			service := updateTarget{Name: "svc", URL: srv.URL, Client: &releasepublish.UpdateClient{BaseURL: srv.URL}}
			conflict, err := (&Server{}).checkVersionConflicts(context.Background(), service, "", bundle, layout)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr {
				if conflict == nil {
					t.Fatal("expected a version conflict")
				}
				if !strings.Contains(conflict.Error(), "replace_existing") {
					t.Fatalf("message should tell the operator how to override: %s", conflict.Error())
				}
				if len(conflict.Cells) != 1 || !strings.Contains(conflict.Cells[0], "mbot/linux/amd64") {
					t.Fatalf("conflict cells = %+v", conflict.Cells)
				}
				return
			}
			if conflict != nil {
				t.Fatalf("unexpected conflict: %v", conflict)
			}
		})
	}
}
