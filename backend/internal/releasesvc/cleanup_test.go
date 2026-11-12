package releasesvc

import (
	"context"
	"testing"
	"time"

	"smartpipe/backend/internal/releasev2"
)

type fakeArtifactStore struct {
	objects []releasev2.StoredObject
	deleted []string
}

func (f *fakeArtifactStore) List(context.Context, string) ([]releasev2.StoredObject, error) {
	return append([]releasev2.StoredObject(nil), f.objects...), nil
}

func (f *fakeArtifactStore) Delete(_ context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	return nil
}

type fakeRunRetentions map[string]releasev2.RunRetention

func (f fakeRunRetentions) ReleaseV2RunRetentions(_ context.Context, ids []string) (map[string]releasev2.RunRetention, error) {
	out := map[string]releasev2.RunRetention{}
	for _, id := range ids {
		if info, ok := f[id]; ok {
			out[id] = info
		}
	}
	return out, nil
}

func ts(t time.Time) *time.Time { return &t }

func TestArtifactJanitorSweepsFailedCancelledAndOrphans(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour)
	fresh := now.Add(-5 * time.Minute)
	objects := &fakeArtifactStore{objects: []releasev2.StoredObject{
		{Key: "releases/rrun_failed/t/app.zip", ModTime: old},
		{Key: "releases/rrun_cancelled/t/app.zip", ModTime: fresh},
		{Key: "releases/rrun_built/t/app.zip", ModTime: old},
		{Key: "releases/rrun_published/t/app.zip", ModTime: old},
		{Key: "releases/rrun_orphan_old/t/app.zip", ModTime: old},
		{Key: "releases/rrun_orphan_fresh/t/app.zip", ModTime: fresh},
		{Key: "other/not-an-artifact.bin", ModTime: old},
	}}
	statuses := fakeRunRetentions{
		"rrun_failed":    {Status: releasev2.RunFailed},
		"rrun_cancelled": {Status: releasev2.RunCancelled},
		"rrun_built":     {Status: releasev2.RunBuilt},
		"rrun_published": {Status: releasev2.RunPublished},
	}
	j := &ArtifactJanitor{Objects: objects, Runs: statuses, Grace: time.Hour, Now: func() time.Time { return now }, Logf: t.Logf}
	n, err := j.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("deleted %d objects, want 3: %v", n, objects.deleted)
	}
	want := map[string]bool{
		"releases/rrun_failed/t/app.zip":     true,
		"releases/rrun_cancelled/t/app.zip":  true,
		"releases/rrun_orphan_old/t/app.zip": true,
	}
	if len(objects.deleted) != len(want) {
		t.Fatalf("deleted = %v", objects.deleted)
	}
	for _, key := range objects.deleted {
		if !want[key] {
			t.Fatalf("unexpected delete %s (all: %v)", key, objects.deleted)
		}
	}
}

func TestArtifactJanitorKeepsFreshOrphansWhenGraceApplies(t *testing.T) {
	now := time.Now()
	objects := &fakeArtifactStore{objects: []releasev2.StoredObject{
		{Key: "releases/rrun_unknown/t/app.zip", ModTime: now},
	}}
	j := &ArtifactJanitor{Objects: objects, Runs: fakeRunRetentions{}, Grace: time.Hour, Now: time.Now, Logf: t.Logf}
	n, err := j.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 || len(objects.deleted) != 0 {
		t.Fatalf("fresh orphan should survive the grace window: n=%d %v", n, objects.deleted)
	}
}

func TestArtifactJanitorRetentionKeepsLatestPublished(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	ago := func(h int) *time.Time { return ts(now.Add(-time.Duration(h) * time.Hour)) }
	objects := &fakeArtifactStore{objects: []releasev2.StoredObject{
		{Key: "releases/rrun_old_built/t/app.zip", ModTime: now},
		{Key: "releases/rrun_new_built/t/app.zip", ModTime: now},
		{Key: "releases/rrun_old_pub_superseded/t/app.zip", ModTime: now},
		{Key: "releases/rrun_new_pub_latest/t/app.zip", ModTime: now},
		{Key: "releases/rrun_old_pub_other_project/t/app.zip", ModTime: now},
	}}
	statuses := fakeRunRetentions{
		"rrun_old_built":              {Status: releasev2.RunBuilt, ProjectID: "p1", Channel: "stable", FinishedAt: ago(30), UpdatedAt: now.Add(-30 * time.Hour)},
		"rrun_new_built":              {Status: releasev2.RunBuilt, ProjectID: "p1", Channel: "stable", FinishedAt: ago(1), UpdatedAt: now.Add(-time.Hour)},
		"rrun_old_pub_superseded":     {Status: releasev2.RunPublished, ProjectID: "p1", Channel: "stable", FinishedAt: ago(40), UpdatedAt: now.Add(-40 * time.Hour)},
		"rrun_new_pub_latest":         {Status: releasev2.RunPublished, ProjectID: "p1", Channel: "stable", FinishedAt: ago(10), UpdatedAt: now.Add(-10 * time.Hour)},
		"rrun_old_pub_other_project":  {Status: releasev2.RunPublished, ProjectID: "p2", Channel: "stable", FinishedAt: ago(40), UpdatedAt: now.Add(-40 * time.Hour)},
	}
	j := &ArtifactJanitor{
		Objects: objects, Runs: statuses, Retention: 24 * time.Hour,
		Now: func() time.Time { return now }, Logf: t.Logf,
	}
	n, err := j.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("deleted %d objects, want 2: %v", n, objects.deleted)
	}
	want := map[string]bool{
		"releases/rrun_old_built/t/app.zip":          true,
		"releases/rrun_old_pub_superseded/t/app.zip": true,
	}
	for _, key := range objects.deleted {
		if !want[key] {
			t.Fatalf("unexpected delete %s (all: %v)", key, objects.deleted)
		}
	}
}

func TestArtifactJanitorRetentionDisabledKeepsBuiltAndPublished(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	objects := &fakeArtifactStore{objects: []releasev2.StoredObject{
		{Key: "releases/rrun_built/t/app.zip", ModTime: now},
		{Key: "releases/rrun_pub/t/app.zip", ModTime: now},
	}}
	statuses := fakeRunRetentions{
		"rrun_built": {Status: releasev2.RunBuilt, ProjectID: "p1", Channel: "stable"},
		"rrun_pub":   {Status: releasev2.RunPublished, ProjectID: "p1", Channel: "stable"},
	}
	j := &ArtifactJanitor{Objects: objects, Runs: statuses, Now: func() time.Time { return now }, Logf: t.Logf}
	if n, err := j.Sweep(context.Background()); err != nil || n != 0 {
		t.Fatalf("retention disabled should not delete: n=%d err=%v %v", n, err, objects.deleted)
	}
}

func TestRunIDFromObjectKey(t *testing.T) {
	cases := map[string]string{
		"releases/rrun_abc/t/1/role/app.zip": "rrun_abc",
		"releases/rrun_abc":                  "",
		"other/rrun_abc/t/app.zip":           "",
		"releases/not-a-run/t/app.zip":       "",
	}
	for key, want := range cases {
		if got := runIDFromObjectKey(key); got != want {
			t.Fatalf("runIDFromObjectKey(%q) = %q, want %q", key, got, want)
		}
	}
}