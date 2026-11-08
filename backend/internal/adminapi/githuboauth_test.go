package adminapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitHubOAuthHelpers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/device/code":
			_ = r.ParseForm()
			if r.Form.Get("client_id") != "cid" || r.Form.Get("scope") != "public_repo" {
				t.Errorf("unexpected form: %v", r.Form)
			}
			_, _ = w.Write([]byte(`{"device_code":"dc","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`))
		case "/login/oauth/access_token":
			_ = r.ParseForm()
			if r.Form.Get("device_code") != "dc" {
				t.Errorf("unexpected device code: %v", r.Form)
			}
			_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
		case "/user":
			if got := r.Header.Get("Authorization"); got != "Bearer tok" {
				t.Errorf("auth header = %q", got)
			}
			_, _ = w.Write([]byte(`{"login":"jacks001314"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	oldOAuth, oldAPI := githubOAuthBase, githubAPIBase
	githubOAuthBase, githubAPIBase = srv.URL, srv.URL
	defer func() { githubOAuthBase, githubAPIBase = oldOAuth, oldAPI }()

	code, err := requestDeviceCode(context.Background(), "cid", "public_repo")
	if err != nil {
		t.Fatalf("device code: %v", err)
	}
	if code.UserCode != "ABCD-1234" || code.Interval != 5 {
		t.Fatalf("unexpected: %+v", code)
	}

	tok, err := pollAccessToken(context.Background(), "cid", "dc")
	if err != nil || tok.Error != "authorization_pending" {
		t.Fatalf("poll: %+v %v", tok, err)
	}

	login, err := fetchGitHubLogin(context.Background(), "tok")
	if err != nil || login != "jacks001314" {
		t.Fatalf("login = %q, %v", login, err)
	}
}
