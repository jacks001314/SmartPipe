package adminapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSplitGroupsTrimsAndDropsEmpty(t *testing.T) {
	got := splitGroups(" grp-a, ,grp-b ")
	if len(got) != 2 || got[0] != "grp-a" || got[1] != "grp-b" {
		t.Fatalf("unexpected groups: %#v", got)
	}
}

func TestRemoveGroupKeepsOthers(t *testing.T) {
	got := removeGroup([]string{"a", "b", "c"}, "b")
	if len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("unexpected groups: %#v", got)
	}
}

func TestLanxinBotCRUDSyncsGroups(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/apptoken/create":
			_, _ = w.Write([]byte(`{"errCode":0,"data":{"appToken":"tok","expiresIn":7200}}`))
		case r.URL.Path == "/v2/groups/fetch":
			_, _ = w.Write([]byte(`{"errCode":0,"data":{"totalGroupIds":2,"groupIds":["grp-1","grp-2"]}}`))
		case strings.HasSuffix(r.URL.Path, "/info/fetch"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v2/groups/"), "/info/fetch")
			_ = json.NewEncoder(w).Encode(map[string]any{"errCode": 0, "data": map[string]any{
				"name": "群-" + id, "owner": map[string]string{"staffId": "u-1", "name": "Alice"},
				"totalMembers": 12, "sendMsgStatus": true,
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer fake.Close()

	app, _ := newTestAPI(t, "")
	rec, body := do(t, app, http.MethodPost, "/admin/lanxin/bots", "", map[string]any{
		"name": "研发机器人", "app_id": "app-1", "app_secret": "secret-1", "api_base": fake.URL,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret-1") {
		t.Fatal("response leaked app secret")
	}
	bot := body["data"].(map[string]any)
	botID := bot["id"].(string)
	if botID == "" || bot["group_count"].(float64) != 2 {
		t.Fatalf("unexpected bot: %+v", bot)
	}

	rec, body = do(t, app, http.MethodGet, "/admin/lanxin/bots", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d", rec.Code)
	}
	if got := len(body["data"].([]any)); got != 1 {
		t.Fatalf("list count=%d", got)
	}

	rec, body = do(t, app, http.MethodGet, "/admin/lanxin/groups?bot_id="+botID, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("groups status=%d body=%s", rec.Code, rec.Body.String())
	}
	groups := body["data"].([]any)
	if len(groups) != 2 {
		t.Fatalf("groups=%d", len(groups))
	}

	rec, body = do(t, app, http.MethodGet, "/admin/lanxin/bots/"+botID, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", rec.Code, rec.Body.String())
	}
	detail := body["data"].(map[string]any)
	if len(detail["groups"].([]any)) != 2 {
		t.Fatalf("detail groups=%v", detail["groups"])
	}

	rec, body = do(t, app, http.MethodPost, "/admin/projects", "", map[string]any{
		"name": "蓝信选群项目", "github_issue_repo": "example/repo",
		"lanxin_bot_id": botID, "bluex_group_ids": "grp-1",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project status=%d body=%s", rec.Code, rec.Body.String())
	}
	project := body["data"].(map[string]any)
	if project["lanxin_bot_id"] != botID || project["bluex_group_ids"] != "grp-1" {
		t.Fatalf("project binding not persisted: %+v", project)
	}
	rec, _ = do(t, app, http.MethodPost, "/admin/projects", "", map[string]any{
		"name": "重复选群项目", "github_issue_repo": "example/repo2",
		"lanxin_bot_id": botID, "bluex_group_ids": "grp-1",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate group binding status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec, _ = do(t, app, http.MethodDelete, "/admin/lanxin/bots/"+botID, "", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
}
