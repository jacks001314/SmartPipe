package releasemcp

import (
	"context"
	"testing"
)

type fakeSettings map[string]string

func (f fakeSettings) GetSetting(_ context.Context, key string) (string, error) { return f[key], nil }

// 界面里保存的升级服务地址优先于启动时的环境变量。
func TestUpdateClientUsesSavedSettings(t *testing.T) {
	s := &Server{Settings: fakeSettings{"release_update_url": "https://update.example.com"}}
	client := s.updateClient(context.Background())
	if client.BaseURL != "https://update.example.com" {
		t.Fatalf("base = %q", client.BaseURL)
	}
	// 没配置时回落到环境变量里的默认值。
	s2 := &Server{Settings: fakeSettings{}, UpdateURL: "https://env.example.com"}
	if got := s2.updateClient(context.Background()).BaseURL; got != "https://env.example.com" {
		t.Fatalf("fallback base = %q", got)
	}
}

// GitHub 凭据与 API 地址同样从设置读取。
func TestGitHubCredentialAndBaseFromSettings(t *testing.T) {
	s := &Server{Settings: fakeSettings{
		"release_github_api_base":   "https://github.example.com/api/v3",
		"release_github_credential": "gh-prod",
	}}
	args := map[string]any{}
	if got := s.githubCredential(context.Background(), args); got != "gh-prod" {
		t.Fatalf("credential = %q", got)
	}
	client := s.githubClient(context.Background())
	if client.APIBase != "https://github.example.com/api/v3" || client.UploadBase != client.APIBase {
		t.Fatalf("client base = %q upload = %q", client.APIBase, client.UploadBase)
	}
	// 工具参数优先于设置。
	if got := s.githubCredential(context.Background(), map[string]any{"credential": "explicit"}); got != "explicit" {
		t.Fatalf("explicit credential = %q", got)
	}
}
