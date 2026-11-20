package testlab

import "testing"

func TestParseStoryMarkdown(t *testing.T) {
	parsed := ParseStoryMarkdown("install-smoke.md", `---
id: install-smoke
title: 安装完好性与主界面
mode: story
environment: syntropy
platform: windows-amd64
required_checks:
  - app_installed
  - app_window
tags: [smoke, install]
timeout_seconds: 900
---
# 步骤
1. 确认安装目录
`)
	if parsed.Slug != "install-smoke" || parsed.Title != "安装完好性与主界面" {
		t.Fatalf("unexpected metadata: %+v", parsed)
	}
	if len(parsed.RequiredChecks) != 2 || parsed.RequiredChecks[0] != "app_installed" {
		t.Fatalf("checks: %+v", parsed.RequiredChecks)
	}
	if len(parsed.Tags) != 2 || parsed.TimeoutSeconds != 900 || parsed.Mode != "story" {
		t.Fatalf("tags/timeouts: %+v", parsed)
	}
	if parsed.Body == "" || parsed.Body[0] != '#' {
		t.Fatalf("body not split: %q", parsed.Body)
	}
}

func TestParseStoryMarkdownFallsBackToFilename(t *testing.T) {
	parsed := ParseStoryMarkdown(`C:\stories\Hot Swap Smoke.md`, "# body")
	if parsed.Slug != "hot-swap-smoke" || parsed.Title != "Hot Swap Smoke" {
		t.Fatalf("fallback failed: %+v", parsed)
	}
	if parsed.Environment != "syntropy" || parsed.Platform != "windows-amd64" || parsed.Mode != "story" {
		t.Fatalf("defaults missing: %+v", parsed)
	}
}

func TestStoryValidation(t *testing.T) {
	good := Story{ProjectID: "p", Slug: "a", Title: "t", Body: "b", Mode: "story", Environment: "syntropy", Platform: "windows-amd64"}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []Story{
		{ProjectID: "p", Slug: "../x", Title: "t", Body: "b", Mode: "story", Environment: "e", Platform: "p"},
		{ProjectID: "p", Slug: "a", Title: "", Body: "b", Mode: "story", Environment: "e", Platform: "p"},
		{ProjectID: "p", Slug: "a", Title: "t", Body: "b", Mode: "nope", Environment: "e", Platform: "p"},
		{ProjectID: "", Slug: "a", Title: "t", Body: "b", Mode: "story", Environment: "e", Platform: "p"},
	}
	for _, story := range bad {
		if err := story.Validate(); err == nil {
			t.Fatalf("invalid story accepted: %+v", story)
		}
	}
}

func TestParseStoryMarkdownEvents(t *testing.T) {
	parsed := ParseStoryMarkdown("settings.md", `---
id: settings
title: 设置冒烟
events:
  - id: open-settings
    title: 打开设置
    trigger_kind: ui
    trigger: 点击左上角设置
    method: settings_open
    expected: 设置页可见
  - title: 切换主题
    trigger_kind: shell
    trigger: powershell -File theme.ps1
    method: theme_switch
tags: [ui]
---
# 正文
`)
	if len(parsed.Events) != 2 {
		t.Fatalf("events not parsed: %+v", parsed.Events)
	}
	first := parsed.Events[0]
	if first.ID != "open-settings" || first.Method != "settings_open" || first.TriggerKind != "ui" ||
		first.Trigger != "点击左上角设置" || first.Expected != "设置页可见" {
		t.Fatalf("event fields: %+v", first)
	}
	if second := parsed.Events[1]; second.ID != "event-2" || second.Method != "theme_switch" || second.TriggerKind != "shell" {
		t.Fatalf("derived event id: %+v", second)
	}
	if len(parsed.RequiredChecks) != 2 || parsed.RequiredChecks[0] != "settings_open" || parsed.RequiredChecks[1] != "theme_switch" {
		t.Fatalf("event methods should become required checks: %+v", parsed.RequiredChecks)
	}
	if len(parsed.Tags) != 1 || parsed.Tags[0] != "ui" {
		t.Fatalf("tags after events block: %+v", parsed.Tags)
	}
}

func TestStoryEventValidation(t *testing.T) {
	good := []StoryEvent{{ID: "a", Title: "A", Method: "check_a", TriggerKind: "ui"}}
	if err := ValidateEvents(good); err != nil {
		t.Fatal(err)
	}
	bad := map[string][]StoryEvent{
		"duplicate id":     {{ID: "a", Title: "A", Method: "x"}, {ID: "a", Title: "B", Method: "y"}},
		"duplicate method": {{ID: "a", Title: "A", Method: "x"}, {ID: "b", Title: "B", Method: "x"}},
		"bad method":       {{ID: "a", Title: "A", Method: "bad method!"}},
		"bad trigger":      {{ID: "a", Title: "A", Method: "x", TriggerKind: "sms"}},
		"empty title":      {{ID: "a", Title: " ", Method: "x"}},
	}
	for name, events := range bad {
		if err := ValidateEvents(events); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestSessionSpecEventMethodsMustBeRequiredChecks(t *testing.T) {
	spec := SessionSpec{ProjectID: "p", Title: "t", Environment: "syntropy", Platform: "windows-amd64",
		Story: "body", RequiredChecks: []string{"settings_open"},
		Events: []StoryEvent{{ID: "open-settings", Title: "打开设置", Method: "settings_open"}}}
	if err := spec.Validate(); err != nil {
		t.Fatalf("valid event session rejected: %v", err)
	}
	spec.RequiredChecks = []string{"other"}
	if err := spec.Validate(); err == nil {
		t.Fatal("event method that is not a required check was accepted")
	}
}
