package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"smartpipe/backend/internal/testlab"
)

func init() {
	migrations = append(migrations, migration{version: 59, name: "test_story_library", sql: `
CREATE TABLE IF NOT EXISTS test_stories (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 slug TEXT NOT NULL,
 title TEXT NOT NULL,
 body TEXT NOT NULL,
 mode TEXT NOT NULL DEFAULT 'story',
 environment TEXT NOT NULL DEFAULT 'syntropy',
 platform TEXT NOT NULL DEFAULT 'windows-amd64',
 required_checks JSONB NOT NULL DEFAULT '[]',
 tags JSONB NOT NULL DEFAULT '[]',
 timeout_seconds INT NOT NULL DEFAULT 1800,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(project_id,slug)
);
CREATE INDEX IF NOT EXISTS test_stories_project_idx ON test_stories(project_id,updated_at DESC);
`})
	migrations = append(migrations, migration{version: 61, name: "test_story_events", sql: `
ALTER TABLE test_stories ADD COLUMN IF NOT EXISTS events JSONB NOT NULL DEFAULT '[]';
`})
}

func normalizeStory(story testlab.Story) (testlab.Story, error) {
	if story.Mode == "" {
		story.Mode = "story"
	}
	if story.Environment == "" {
		story.Environment = "syntropy"
	}
	if story.Platform == "" {
		story.Platform = "windows-amd64"
	}
	if story.TimeoutSeconds == 0 {
		story.TimeoutSeconds = 30 * 60
	}
	if story.RequiredChecks == nil {
		story.RequiredChecks = []string{}
	}
	if story.Tags == nil {
		story.Tags = []string{}
	}
	if story.Events == nil {
		story.Events = []testlab.StoryEvent{}
	}
	return story, story.Validate()
}

func (s *Store) CreateTestStory(ctx context.Context, story testlab.Story) (*testlab.Story, error) {
	story, err := normalizeStory(story)
	if err != nil {
		return nil, err
	}
	if story.ID == "" {
		story.ID = newID("story")
	}
	checks, _ := json.Marshal(story.RequiredChecks)
	tags, _ := json.Marshal(story.Tags)
	events, _ := json.Marshal(story.Events)
	_, err = s.db.ExecContext(ctx, `INSERT INTO test_stories(id,project_id,slug,title,body,mode,environment,platform,required_checks,tags,timeout_seconds,events)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, story.ID, story.ProjectID, story.Slug, story.Title, story.Body,
		story.Mode, story.Environment, story.Platform, checks, tags, story.TimeoutSeconds, events)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, fmt.Errorf("%w: slug %s already exists in this project", ErrConflict, story.Slug)
		}
		return nil, err
	}
	return s.GetTestStory(ctx, story.ID)
}

// UpsertImportedTestStory makes Markdown import idempotent per (project, slug).
func (s *Store) UpsertImportedTestStory(ctx context.Context, story testlab.Story) (*testlab.Story, error) {
	story, err := normalizeStory(story)
	if err != nil {
		return nil, err
	}
	if story.ID == "" {
		story.ID = newID("story")
	}
	checks, _ := json.Marshal(story.RequiredChecks)
	tags, _ := json.Marshal(story.Tags)
	events, _ := json.Marshal(story.Events)
	err = s.db.QueryRowContext(ctx, `INSERT INTO test_stories(id,project_id,slug,title,body,mode,environment,platform,required_checks,tags,timeout_seconds,events)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
 ON CONFLICT(project_id,slug) DO UPDATE SET title=EXCLUDED.title,body=EXCLUDED.body,mode=EXCLUDED.mode,
  environment=EXCLUDED.environment,platform=EXCLUDED.platform,required_checks=EXCLUDED.required_checks,
  tags=EXCLUDED.tags,timeout_seconds=EXCLUDED.timeout_seconds,events=EXCLUDED.events,updated_at=now()
 RETURNING id`, story.ID, story.ProjectID, story.Slug, story.Title, story.Body, story.Mode, story.Environment,
		story.Platform, checks, tags, story.TimeoutSeconds, events).Scan(&story.ID)
	if err != nil {
		return nil, err
	}
	return s.GetTestStory(ctx, story.ID)
}

func (s *Store) UpdateTestStory(ctx context.Context, story testlab.Story) (*testlab.Story, error) {
	story, err := normalizeStory(story)
	if err != nil {
		return nil, err
	}
	checks, _ := json.Marshal(story.RequiredChecks)
	tags, _ := json.Marshal(story.Tags)
	events, _ := json.Marshal(story.Events)
	res, err := s.db.ExecContext(ctx, `UPDATE test_stories SET slug=$2,title=$3,body=$4,mode=$5,environment=$6,
 platform=$7,required_checks=$8,tags=$9,timeout_seconds=$10,events=$11,updated_at=now() WHERE id=$1`,
		story.ID, story.Slug, story.Title, story.Body, story.Mode, story.Environment, story.Platform, checks, tags, story.TimeoutSeconds, events)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, fmt.Errorf("%w: slug %s already exists in this project", ErrConflict, story.Slug)
		}
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return nil, ErrNotFound
	}
	return s.GetTestStory(ctx, story.ID)
}

func (s *Store) GetTestStory(ctx context.Context, id string) (*testlab.Story, error) {
	var story testlab.Story
	var checks, tags, events []byte
	var created, updated sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT id,project_id,slug,title,body,mode,environment,platform,required_checks,tags,timeout_seconds,events,created_at,updated_at
 FROM test_stories WHERE id=$1`, id).Scan(&story.ID, &story.ProjectID, &story.Slug, &story.Title, &story.Body,
		&story.Mode, &story.Environment, &story.Platform, &checks, &tags, &story.TimeoutSeconds, &events, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(checks, &story.RequiredChecks)
	_ = json.Unmarshal(tags, &story.Tags)
	_ = json.Unmarshal(events, &story.Events)
	if story.RequiredChecks == nil {
		story.RequiredChecks = []string{}
	}
	if story.Tags == nil {
		story.Tags = []string{}
	}
	if story.Events == nil {
		story.Events = []testlab.StoryEvent{}
	}
	if created.Valid {
		story.CreatedAt = created.Time.UTC().Format("2006-01-02T15:04:05Z")
	}
	if updated.Valid {
		story.UpdatedAt = updated.Time.UTC().Format("2006-01-02T15:04:05Z")
	}
	return &story, nil
}

func (s *Store) ListTestStories(ctx context.Context, projectID, query string) ([]testlab.Story, error) {
	query = strings.TrimSpace(query)
	rows, err := s.db.QueryContext(ctx, `SELECT id,project_id,slug,title,body,mode,environment,platform,required_checks,tags,timeout_seconds,events,created_at,updated_at
 FROM test_stories WHERE ($1='' OR project_id=$1)
 AND ($2='' OR title ILIKE '%'||$2||'%' OR slug ILIKE '%'||$2||'%')
 ORDER BY updated_at DESC LIMIT 200`, projectID, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []testlab.Story{}
	for rows.Next() {
		var story testlab.Story
		var checks, tags, events []byte
		var created, updated sql.NullTime
		if err := rows.Scan(&story.ID, &story.ProjectID, &story.Slug, &story.Title, &story.Body, &story.Mode,
			&story.Environment, &story.Platform, &checks, &tags, &story.TimeoutSeconds, &events, &created, &updated); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(checks, &story.RequiredChecks)
		_ = json.Unmarshal(tags, &story.Tags)
		_ = json.Unmarshal(events, &story.Events)
		if story.RequiredChecks == nil {
			story.RequiredChecks = []string{}
		}
		if story.Tags == nil {
			story.Tags = []string{}
		}
		if story.Events == nil {
			story.Events = []testlab.StoryEvent{}
		}
		if updated.Valid {
			story.UpdatedAt = updated.Time.UTC().Format("2006-01-02T15:04:05Z")
		}
		out = append(out, story)
	}
	return out, rows.Err()
}

func (s *Store) DeleteTestStory(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM test_stories WHERE id=$1`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrNotFound
	}
	return nil
}
