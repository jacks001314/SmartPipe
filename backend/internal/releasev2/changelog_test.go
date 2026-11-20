package releasev2

import (
	"strings"
	"testing"
)

func TestRenderChangelogGroupsAndSkipsNoise(t *testing.T) {
	commits := []ChangelogCommit{
		{Message: "feat(computer-use): 新增 Windows 原生电脑操控"},
		{Message: "fix(desktop): 修复窗口关闭后进程残留"},
		{Message: "perf(bundle): 减少启动时的重复解压"},
		{Message: "docs(release): 补充安装说明"},
		{Message: "test(qa): 补齐真实进程退出夹具"},
		{Message: "chore(release): bump 0.5.22"},
		{Message: "Merge pull request #466 from Ch3nYe/codex/evo-valley-ux"},
		{Message: "feat(computer-use): 新增 Windows 原生电脑操控"},
		{Message: "随手改一下文案"},
	}
	got := RenderChangelog("v0.5.21", "v0.5.22", "https://example.com/compare", commits, 25)
	for _, want := range []string{
		"## 更新日志",
		"### 新功能",
		"- computer-use：新增 Windows 原生电脑操控",
		"### 修复",
		"- desktop：修复窗口关闭后进程残留",
		"### 优化",
		"- bundle：减少启动时的重复解压",
		"### 文档",
		"- release：补充安装说明",
		"### 其他",
		"- 随手改一下文案",
		"[完整变更：v0.5.21 → v0.5.22](https://example.com/compare)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("changelog missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"test(qa)", "chore(release)", "Merge pull request"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("changelog should skip %q:\n%s", unwanted, got)
		}
	}
	if strings.Count(got, "computer-use：新增 Windows 原生电脑操控") != 1 {
		t.Fatalf("duplicate commit must be listed once:\n%s", got)
	}
}

func TestRenderChangelogKeepsNewestItems(t *testing.T) {
	commits := make([]ChangelogCommit, 0, 5)
	for _, subject := range []string{"fix: 第一条", "fix: 第二条", "fix: 第三条"} {
		commits = append(commits, ChangelogCommit{Message: subject})
	}
	got := RenderChangelog("v1", "v2", "", commits, 2)
	if strings.Contains(got, "第一条") {
		t.Fatalf("oldest item must be dropped when truncated:\n%s", got)
	}
	if !strings.Contains(got, "第二条") || !strings.Contains(got, "第三条") {
		t.Fatalf("newest items missing:\n%s", got)
	}
	if !strings.Contains(got, "已省略部分提交") {
		t.Fatalf("truncation note missing:\n%s", got)
	}
}

func TestRenderChangelogWithoutMeaningfulCommits(t *testing.T) {
	got := RenderChangelog("v1", "v2", "", []ChangelogCommit{{Message: "test: only tests"}}, 25)
	if !strings.Contains(got, "没有需要展开的功能性改动") {
		t.Fatalf("empty changelog = %q", got)
	}
}

func TestMergeChangelogKeepsGeneratedSection(t *testing.T) {
	existing := "# Syntropy v0.5.22\n\n## 更新日志\n\n### 修复\n\n- 修复窗口关闭后进程残留\n\n## 安装包\n\n- Windows x64"
	updated := "Syntropy 0.5.22\n\n- Windows x64 安装包"
	got := MergeChangelog(existing, updated)
	if !strings.Contains(got, "## 更新日志") || !strings.Contains(got, "修复窗口关闭后进程残留") {
		t.Fatalf("changelog section lost: %s", got)
	}
	if strings.Contains(got, "## 安装包") {
		t.Fatalf("only the changelog section should be carried over: %s", got)
	}
}

func TestMergeChangelogLeavesUpdatedSectionAlone(t *testing.T) {
	existing := "## 更新日志\n\n- 旧条目"
	updated := "## 更新日志\n\n- 新条目"
	if got := MergeChangelog(existing, updated); got != updated {
		t.Fatalf("updated notes = %q, want %q", got, updated)
	}
}
