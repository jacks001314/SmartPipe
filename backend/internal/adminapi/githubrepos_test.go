package adminapi

import (
	"testing"
	"time"

	gh "github.com/google/go-github/v68/github"
)

func TestGitHubRepoViewMapsBasicFields(t *testing.T) {
	updated := gh.Timestamp{Time: time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)}
	repo := &gh.Repository{
		ID:              gh.Int64(42),
		FullName:        gh.String("jacks001314/SmartPipe"),
		Name:            gh.String("SmartPipe"),
		Owner:           &gh.User{Login: gh.String("jacks001314")},
		Private:         gh.Bool(true),
		Description:     gh.String("pipeline"),
		DefaultBranch:   gh.String("main"),
		Language:        gh.String("Go"),
		StargazersCount: gh.Int(7),
		OpenIssuesCount: gh.Int(3),
		UpdatedAt:       &updated,
		HTMLURL:         gh.String("https://github.com/jacks001314/SmartPipe"),
		Archived:        gh.Bool(false),
		Fork:            gh.Bool(false),
	}
	view := repoView(repo)
	if view.FullName != "jacks001314/SmartPipe" || view.Owner != "jacks001314" || view.Name != "SmartPipe" {
		t.Fatalf("unexpected identity: %#v", view)
	}
	if !view.Private || view.DefaultBranch != "main" || view.Language != "Go" {
		t.Fatalf("unexpected repo flags: %#v", view)
	}
	if view.Stars != 7 || view.OpenIssues != 3 || view.UpdatedAt != "2026-11-08T10:00:00Z" {
		t.Fatalf("unexpected metrics: %#v", view)
	}
	if view.HTMLURL == "" {
		t.Fatal("missing html url")
	}
}

func TestMergeGitHubRepoViewsPrefersAppAndDedupes(t *testing.T) {
	index := map[string]int{}
	views := mergeGitHubRepoViews(nil, index, []githubRepoView{
		{FullName: "jacks001314/SmartPipe", Owner: "jacks001314"},
		{FullName: "Ch3nYe/Syntropy", Owner: "Ch3nYe"},
	}, "user")
	if len(views) != 2 || views[0].Source != "user" {
		t.Fatalf("unexpected first merge: %#v", views)
	}
	views = mergeGitHubRepoViews(views, index, []githubRepoView{
		{FullName: "jacks001314/SmartPipe", Owner: "jacks001314"},
	}, "app")
	if len(views) != 2 {
		t.Fatalf("expected dedupe, got %d", len(views))
	}
	if views[0].Source != "app" || views[1].Source != "user" {
		t.Fatalf("expected app to win, got %#v", views)
	}
	if views[1].FullName != "Ch3nYe/Syntropy" {
		t.Fatalf("invited repo lost: %#v", views[1])
	}
}
