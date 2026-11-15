package releasemcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"smartpipe/backend/internal/releasepublish"
)

type staticCreds string

func (s staticCreds) Resolve(context.Context, string) (string, error) { return string(s), nil }

func TestRollbackChannelRepublishesPreviousPointer(t *testing.T) {
	var published []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/distributions/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"payload": map[string]any{
					"channel": "beta", "productVersion": "0.5.16", "bomSha256": "bom-old",
					"components": []map[string]string{{"name": "product", "version": "0.5.16"}},
				},
				"signature": "sig",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/distributions":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			published = append(published, body)
			_ = json.NewEncoder(w).Encode(map[string]any{"channel": "beta", "productVersion": "0.5.16"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	target := updateTarget{
		Name: "primary", URL: server.URL,
		Client: &releasepublish.UpdateClient{BaseURL: server.URL, Creds: staticCreds("token")},
	}
	previous := &releasepublish.ChannelPointer{
		Channel: "beta", ProductVersion: "0.5.16", BOMSHA256: "bom-old",
		Components: []releasepublish.DistributionComponent{{Name: "product", Version: "0.5.16"}},
	}
	s := &Server{}
	status := s.rollbackChannel(context.Background(), target, "cred", "beta", previous)
	if !strings.Contains(status, "已回滚到 0.5.16") {
		t.Fatalf("status = %q", status)
	}
	if len(published) != 1 || published[0]["productVersion"] != "0.5.16" {
		t.Fatalf("published = %+v", published)
	}
}

func TestRollbackChannelWithoutPreviousPointerIsSkipped(t *testing.T) {
	s := &Server{}
	if got := s.rollbackChannel(context.Background(), updateTarget{}, "cred", "beta", nil); !strings.Contains(got, "回滚跳过") {
		t.Fatalf("status = %q", got)
	}
}
