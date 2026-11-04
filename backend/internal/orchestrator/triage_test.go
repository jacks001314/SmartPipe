package orchestrator

import (
	"context"
	"strings"
	"testing"

	"smartpipe/backend/internal/bot"
	"smartpipe/backend/internal/issue"
)

type fakeIssues struct {
	created  []issue.CreateInput
	comments map[string][]string
	search   []issue.Issue
}

func (f *fakeIssues) Create(_ context.Context, in issue.CreateInput) (*issue.Issue, error) {
	f.created = append(f.created, in)
	return &issue.Issue{ID: "ISS-T-0001", Title: in.Title, Description: in.Description, Status: issue.StatusNew}, nil
}

func (f *fakeIssues) Search(_ context.Context, _ string, _ int) ([]issue.Issue, error) {
	return f.search, nil
}

func (f *fakeIssues) AddComment(_ context.Context, id, author, text string) error {
	if f.comments == nil {
		f.comments = map[string][]string{}
	}
	f.comments[id] = append(f.comments[id], author+": "+text)
	return nil
}

func sampleMsg(text string) bot.Inbound {
	return bot.Inbound{
		Platform: "lanxin", PlatformMessageID: "m-1", ChatType: "group",
		SenderID: "s-1", SenderName: "张三", GroupID: "g-1",
		Text: text, IsAtMe: true,
	}
}

func TestTriageCreatesWhenNoSimilar(t *testing.T) {
	fi := &fakeIssues{}
	rt := &RuleTriage{Issues: fi, MergeThreshold: 0.65, RelatedThreshold: 0.30}

	res, err := rt.Triage(context.Background(), sampleMsg("导出报表乱码\n点导出按钮之后全是问号"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != "created" || res.IssueID != "ISS-T-0001" {
		t.Fatalf("action=%s id=%s", res.Action, res.IssueID)
	}
	if len(fi.created) != 1 {
		t.Fatalf("created=%d", len(fi.created))
	}
	in := fi.created[0]
	if in.Title == "" || strings.Contains(in.Title, "\n") {
		t.Fatalf("title 应取首行: %q", in.Title)
	}
	if in.Reporter.UserID != "s-1" || in.SourceMsgID != "m-1" {
		t.Fatalf("reporter/source: %+v", in)
	}
}

func TestTriageMergesOnHighSimilarity(t *testing.T) {
	fi := &fakeIssues{search: []issue.Issue{{
		ID: "ISS-T-0009", Title: "登录失败",
		Description: "登录页面点击登录按钮无响应，无法进入首页",
	}}}
	rt := &RuleTriage{Issues: fi, MergeThreshold: 0.65, RelatedThreshold: 0.30}

	res, err := rt.Triage(context.Background(), sampleMsg("登录失败 无法进入首页"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != "merged" || res.IssueID != "ISS-T-0009" {
		t.Fatalf("action=%s id=%s", res.Action, res.IssueID)
	}
	if len(fi.created) != 0 {
		t.Fatalf("归并不应新建 Issue")
	}
	if cs := fi.comments["ISS-T-0009"]; len(cs) != 1 || !strings.Contains(cs[0], "归并") {
		t.Fatalf("merge comment: %v", fi.comments)
	}
}

func TestTriageCreateListsRelated(t *testing.T) {
	fi := &fakeIssues{search: []issue.Issue{{
		ID: "ISS-T-0007", Title: "导出报表乱码",
		Description: "Windows 下导出 Excel 报表显示问号",
	}}}
	rt := &RuleTriage{Issues: fi, MergeThreshold: 0.65, RelatedThreshold: 0.30}

	res, err := rt.Triage(context.Background(), sampleMsg("导出报表乱码，中文全是问号"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != "created" {
		t.Fatalf("action=%s", res.Action)
	}
	if len(res.SimilarIssues) != 1 || res.SimilarIssues[0].ID != "ISS-T-0007" {
		t.Fatalf("related: %+v", res.SimilarIssues)
	}
}
