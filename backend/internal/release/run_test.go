package release

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"smartpipe/backend/internal/github"
)

type fakeForge struct {
	issues      []github.Issue
	branchSHA   string
	tags        map[string]string
	createdTags []string
	releases    []*github.Release
	removed     []string
	added       []string
	comments    []string
	deletedRefs []string
	deletedRels []int64
	failAdd     bool
	uploaded    []string
	uploadErr   error
}

func newFakeForge(issues ...github.Issue) *fakeForge {
	return &fakeForge{issues: issues, branchSHA: "abc123def4567890", tags: map[string]string{}}
}

func (f *fakeForge) ListIssuesByLabel(ctx context.Context, repo, label string, limit int) ([]github.Issue, error) {
	return f.issues, nil
}

func (f *fakeForge) BranchSHA(ctx context.Context, repo, ref string) (string, error) {
	return f.branchSHA, nil
}

func (f *fakeForge) TagSHA(ctx context.Context, repo, tag string) (string, error) {
	return f.tags[tag], nil
}

func (f *fakeForge) CreateTagRef(ctx context.Context, repo, tag, sha string) error {
	f.tags[tag] = sha
	f.createdTags = append(f.createdTags, tag)
	return nil
}

func (f *fakeForge) DeleteTagRef(ctx context.Context, repo, tag string) error {
	delete(f.tags, tag)
	f.deletedRefs = append(f.deletedRefs, tag)
	return nil
}

func (f *fakeForge) CreateRelease(ctx context.Context, repo, tag, target, title, body string) (*github.Release, error) {
	rel := &github.Release{ID: 9001, HTMLURL: "https://example.test/releases/9001", TagName: tag, Name: title}
	f.releases = append(f.releases, rel)
	return rel, nil
}

func (f *fakeForge) UploadReleaseAsset(ctx context.Context, repo string, releaseID int64, name string, data []byte) (*github.Asset, error) {
	if f.uploadErr != nil {
		return nil, f.uploadErr
	}
	f.uploaded = append(f.uploaded, name)
	return &github.Asset{ID: 1, Name: name, Size: int64(len(data)),
		BrowserDownloadURL: "https://example.test/releases/9001/assets/" + name}, nil
}

func (f *fakeForge) DeleteRelease(ctx context.Context, repo string, id int64) error {
	f.deletedRels = append(f.deletedRels, id)
	return nil
}

func (f *fakeForge) RemoveLabel(ctx context.Context, repo string, number int, label string) error {
	f.removed = append(f.removed, label)
	return nil
}

func (f *fakeForge) AddLabels(ctx context.Context, repo string, number int, labels []string) error {
	if f.failAdd {
		return errors.New("label service down")
	}
	f.added = append(f.added, labels...)
	return nil
}

func (f *fakeForge) Comment(ctx context.Context, repo string, number int, body string) error {
	f.comments = append(f.comments, body)
	return nil
}

type fakeNotifier struct {
	texts []string
	err   error
}

func (n *fakeNotifier) Notify(ctx context.Context, projectID, text string) error {
	n.texts = append(n.texts, text)
	return n.err
}

func stubBuilder(ok bool, err string) BuilderFunc {
	return func(ctx context.Context, req Request, workRoot string) (*Report, error) {
		dir := filepath.Join(workRoot, "dist")
		_ = os.MkdirAll(dir, 0o755)
		path := filepath.Join(dir, "app.zip")
		_ = os.WriteFile(path, []byte("artifact-bytes"), 0o644)
		rep := &Report{OK: ok, Version: req.Version, Commit: req.Commit, Error: err,
			ManifestSHA256: "sha256:deadbeef", OutputDir: "out/v1",
			Artifacts: []Artifact{{Path: path, Size: 14, SHA256: "sha256:cafe"}}}
		return rep, nil
	}
}

func sampleIssues() []github.Issue {
	return []github.Issue{
		{Number: 12, Title: "登录超时", HTMLURL: "https://example.test/issues/12", User: &github.User{Login: "alice"}},
		{Number: 9, Title: "导出失败", HTMLURL: "https://example.test/issues/9", User: &github.User{Login: "bob"}},
	}
}

func TestBuildNoteListsIssuesAndArtifacts(t *testing.T) {
	note := BuildNote("v1.0.0", []Candidate{{Number: 12, Title: "登录超时", URL: "u", Author: "alice"}},
		[]Artifact{{Path: "dist/app.zip", Size: 42, SHA256: "sha256:cafe"}}, "手动说明", time.Now())
	for _, want := range []string{"v1.0.0", "#12", "登录超时", "@alice", "dist/app.zip", "手动说明"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note missing %q:\n%s", want, note)
		}
	}
}

func TestExecuteReleasesCandidates(t *testing.T) {
	forge := newFakeForge(sampleIssues()...)
	notify := &fakeNotifier{}
	run, err := Execute(context.Background(), RunRequest{ProjectID: "prj", Repo: "acme/core", Version: "v1.0.0"},
		forge, notify, stubBuilder(true, ""), t.TempDir())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if run.Status != "released" {
		t.Fatalf("status = %s (%s)", run.Status, run.Error)
	}
	if len(run.Candidates) != 2 || run.Candidates[0].Number != 9 {
		t.Fatalf("candidates = %+v", run.Candidates)
	}
	if run.Commit != "abc123def4567890" || run.ReleaseURL == "" || run.Tag != "v1.0.0" {
		t.Fatalf("run = %+v", run)
	}
	if len(forge.createdTags) != 1 || len(forge.releases) != 1 {
		t.Fatalf("tags=%v releases=%d", forge.createdTags, len(forge.releases))
	}
	if len(forge.removed) != 2 || len(forge.added) != 2 || len(forge.comments) != 2 {
		t.Fatalf("labels removed=%v added=%v comments=%v", forge.removed, forge.added, forge.comments)
	}
	if !strings.Contains(notify.texts[0], "v1.0.0") || !strings.Contains(notify.texts[0], "#9") {
		t.Fatalf("notify = %s", notify.texts[0])
	}
	if run.NotifyStatus != "sent" {
		t.Fatalf("notify status = %s", run.NotifyStatus)
	}
}

func TestExecuteBuildFailureKeepsCandidates(t *testing.T) {
	forge := newFakeForge(sampleIssues()...)
	notify := &fakeNotifier{}
	run, _ := Execute(context.Background(), RunRequest{ProjectID: "prj", Repo: "acme/core", Version: "v1.0.1"},
		forge, notify, stubBuilder(false, "unit test failed"), t.TempDir())
	if run.Status != "failed" || !strings.Contains(run.Error, "unit test failed") {
		t.Fatalf("run = %+v", run)
	}
	if len(forge.createdTags) != 0 || len(forge.removed) != 0 || len(notify.texts) != 0 {
		t.Fatalf("failure must not publish or consume candidates")
	}
	if run.ManifestSHA256 == "" {
		t.Fatalf("failed run should keep build evidence")
	}
}

func TestExecuteWithoutCandidatesSkips(t *testing.T) {
	forge := newFakeForge()
	run, _ := Execute(context.Background(), RunRequest{ProjectID: "prj", Repo: "acme/core", Version: "v1.0.2"},
		forge, &fakeNotifier{}, stubBuilder(true, ""), t.TempDir())
	if run.Status != "skipped" || !strings.Contains(run.Error, "no candidate") {
		t.Fatalf("run = %+v", run)
	}
}

func TestExecuteDryRunDoesNotPublish(t *testing.T) {
	forge := newFakeForge(sampleIssues()...)
	run, _ := Execute(context.Background(), RunRequest{ProjectID: "prj", Repo: "acme/core", Version: "v1.0.3", DryRun: true},
		forge, &fakeNotifier{}, stubBuilder(true, ""), t.TempDir())
	if run.Status != "built" || len(forge.releases) != 0 || len(forge.removed) != 0 {
		t.Fatalf("run = %+v", run)
	}
}

func TestExecuteWarnsWhenLabelsFail(t *testing.T) {
	forge := newFakeForge(sampleIssues()...)
	forge.failAdd = true
	run, _ := Execute(context.Background(), RunRequest{ProjectID: "prj", Repo: "acme/core", Version: "v1.0.4"},
		forge, &fakeNotifier{}, stubBuilder(true, ""), t.TempDir())
	if run.Status != "released_with_warnings" || !strings.Contains(run.Error, "add label") {
		t.Fatalf("run = %+v", run)
	}
}

func TestExecuteRejectsExistingTagOnOtherCommit(t *testing.T) {
	forge := newFakeForge(sampleIssues()...)
	forge.tags["v1.0.5"] = "ffffffffffff"
	run, _ := Execute(context.Background(), RunRequest{ProjectID: "prj", Repo: "acme/core", Version: "v1.0.5"},
		forge, &fakeNotifier{}, stubBuilder(true, ""), t.TempDir())
	if run.Status != "failed" || !strings.Contains(run.Error, "already points") {
		t.Fatalf("run = %+v", run)
	}
}

func TestRollbackRestoresCandidates(t *testing.T) {
	forge := newFakeForge()
	notify := &fakeNotifier{}
	run := &Run{ID: 7, ProjectID: "prj", Version: "v1.0.0", Status: "released", Tag: "v1.0.0", ReleaseID: 9001,
		Candidates: []Candidate{{Number: 9}, {Number: 12}}}
	if err := Rollback(context.Background(), run, "acme/core", forge, notify, "", ""); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if len(forge.deletedRels) != 1 || len(forge.deletedRefs) != 1 || len(forge.added) != 2 {
		t.Fatalf("rollback calls: rels=%v refs=%v added=%v", forge.deletedRels, forge.deletedRefs, forge.added)
	}
	if !strings.Contains(notify.texts[0], "回滚") {
		t.Fatalf("notify = %s", notify.texts[0])
	}
}

func TestRollbackRejectsUnreleasedRun(t *testing.T) {
	err := Rollback(context.Background(), &Run{ID: 1, Status: "failed"}, "acme/core", newFakeForge(), nil, "", "")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestExecuteUploadsReleaseAssets(t *testing.T) {
	forge := newFakeForge(sampleIssues()...)
	run, err := Execute(context.Background(), RunRequest{ProjectID: "prj", Repo: "acme/core", Version: "v1.2.0"},
		forge, &fakeNotifier{}, stubBuilder(true, ""), t.TempDir())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if run.Status != "released" {
		t.Fatalf("status = %s (%s)", run.Status, run.Error)
	}
	if len(forge.uploaded) != 1 || forge.uploaded[0] != "app.zip" {
		t.Fatalf("uploaded = %v", forge.uploaded)
	}
	if len(run.Artifacts) != 1 || run.Artifacts[0].DownloadURL == "" {
		t.Fatalf("artifact download url missing: %+v", run.Artifacts)
	}
	if run.Artifacts[0].UploadError != "" {
		t.Fatalf("unexpected upload error: %s", run.Artifacts[0].UploadError)
	}
}

func TestExecuteKeepsReleaseWhenUploadFails(t *testing.T) {
	forge := newFakeForge(sampleIssues()...)
	forge.uploadErr = errors.New("asset upload 413")
	run, err := Execute(context.Background(), RunRequest{ProjectID: "prj", Repo: "acme/core", Version: "v1.3.0"},
		forge, &fakeNotifier{}, stubBuilder(true, ""), t.TempDir())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if run.Status != "released" {
		t.Fatalf("release should still succeed, got %s (%s)", run.Status, run.Error)
	}
	if len(run.Artifacts) != 1 || run.Artifacts[0].UploadError == "" || run.Artifacts[0].DownloadURL != "" {
		t.Fatalf("artifact state = %+v", run.Artifacts)
	}
}
