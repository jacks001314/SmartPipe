package adminapi

import (
	"strings"
	"testing"

	"smartpipe/backend/internal/testlab"
)

func TestSelectStoryEvents(t *testing.T) {
	events := []testlab.StoryEvent{
		{ID: "a", Title: "A", Method: "check_a"},
		{ID: "b", Title: "B", Method: "check_b"},
	}
	all, err := selectStoryEvents(events, nil)
	if err != nil || len(all) != 2 {
		t.Fatalf("all events: %+v %v", all, err)
	}
	subset, err := selectStoryEvents(events, []string{"b"})
	if err != nil || len(subset) != 1 || subset[0].ID != "b" {
		t.Fatalf("subset: %+v %v", subset, err)
	}
	if _, err := selectStoryEvents(events, []string{"b", "missing"}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("unknown event id should fail: %v", err)
	}
}

func TestScopeStoryToEvents(t *testing.T) {
	body := "# 故事\n1. 做很多事"
	scoped := scopeStoryToEvents(body, []testlab.StoryEvent{{ID: "a", Title: "A", Method: "check_a"}})
	if !strings.HasPrefix(scoped, body) || !strings.Contains(scoped, "本次运行范围") ||
		!strings.Contains(scoped, "a：A（method: check_a）") {
		t.Fatalf("scoped story missing notes: %q", scoped)
	}
	huge := strings.Repeat("x", testlab.MaxStoryBytes)
	if got := scopeStoryToEvents(huge, []testlab.StoryEvent{{ID: "a", Title: "A", Method: "check_a"}}); got != huge {
		t.Fatal("oversized story must not be extended past the session limit")
	}
}

func TestBuildStorySessionSpec(t *testing.T) {
	story := &testlab.Story{
		ID: "story_x", ProjectID: "p", Slug: "s", Title: "T", Body: "body", Mode: "story",
		Environment: "syntropy", Platform: "windows-amd64", RequiredChecks: []string{"other"},
		Events: []testlab.StoryEvent{
			{ID: "a", Title: "A", Method: "check_a"},
			{ID: "b", Title: "B", Method: "check_b"},
		},
		TimeoutSeconds: 600,
	}
	all, err := buildStorySessionSpec(story, storyRunInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Events) != 2 || !contains(all.RequiredChecks, "other") ||
		!contains(all.RequiredChecks, "check_a") || !contains(all.RequiredChecks, "check_b") {
		t.Fatalf("all-events spec: %+v", all)
	}
	subset, err := buildStorySessionSpec(story, storyRunInput{EventIDs: []string{"b"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(subset.Events) != 1 || subset.Events[0].ID != "b" || len(subset.RequiredChecks) != 1 ||
		subset.RequiredChecks[0] != "check_b" || !strings.Contains(subset.Story, "本次运行范围") {
		t.Fatalf("subset spec: %+v", subset)
	}
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
