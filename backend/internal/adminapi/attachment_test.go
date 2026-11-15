package adminapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUploadIssueAttachmentUsesUserAssetsEndpoint(t *testing.T) {
	var gotName, gotRepositoryID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/example/core":
			_, _ = w.Write([]byte(`{"id":99,"full_name":"example/core","private":true}`))
		case r.Method == http.MethodPost && r.URL.Path == "/user-attachments/assets":
			gotName = r.URL.Query().Get("name")
			gotRepositoryID = r.URL.Query().Get("repository_id")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"url":"https://github.com/user-attachments/assets/xyz"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	defer srv.Close()

	app, _ := newTestAPI(t, "")
	rec, body := do(t, app, http.MethodPost, "/admin/projects", "", map[string]any{
		"name":            "Core",
		"github_repo_url": "https://github.com/example/core",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rec.Code, rec.Body.String())
	}
	projectID := body["data"].(map[string]any)["id"].(string)

	rec, _ = do(t, app, http.MethodPost, "/admin/credentials", "", map[string]any{
		"project_id": projectID, "kind": "github_pat", "name": "pat", "value": "ghp_user_token",
		"metadata": map[string]any{"api_base": srv.URL},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create credential: %d %s", rec.Code, rec.Body.String())
	}

	png := []byte("\x89PNG\r\n\x1a\n0000")
	rec, body = do(t, app, http.MethodPost, "/admin/issues/attachments", "", map[string]any{
		"project_id": projectID, "name": "shot.png", "mime_type": "image/png", "data": png,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	data, _ := body["data"].(map[string]any)
	if data["url"] != "https://github.com/user-attachments/assets/xyz" {
		t.Fatalf("data = %v", data)
	}
	if gotName != "shot.png" || gotRepositoryID != "99" {
		t.Fatalf("name=%q repository_id=%q", gotName, gotRepositoryID)
	}
}

func TestAttachmentUploadNameAndMime(t *testing.T) {
	if got := attachmentUploadName("../../shot one.png", "image/png"); got != "shot-one.png" {
		t.Fatalf("name = %q", got)
	}
	if got := attachmentUploadName("", "image/jpeg"); got != "attachment.jpg" {
		t.Fatalf("fallback name = %q", got)
	}
	if got := attachmentMimeType("image/png; charset=binary", nil); got != "image/png" {
		t.Fatalf("mime = %q", got)
	}
}
