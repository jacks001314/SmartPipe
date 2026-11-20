package adminapi

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/testlab"
)

// The story library is the authoring surface behind the console: import or edit
// Markdown, then trigger a session from it. A run snapshots the body into the
// immutable SessionSpec, so later edits never rewrite history.
func (a *API) registerTestStories(admin iris.Party) {
	admin.Get("/test-stories", a.listTestStories)
	admin.Post("/test-stories", a.createTestStory)
	admin.Post("/test-stories/import", a.importTestStories)
	admin.Get("/test-stories/{id}", a.getTestStory)
	admin.Put("/test-stories/{id}", a.updateTestStory)
	admin.Delete("/test-stories/{id}", a.deleteTestStory)
	admin.Post("/test-stories/{id}/run", a.runTestStory)
	admin.Post("/test-stories/run-batch", a.runTestStoriesBatch)
}

func (a *API) listTestStories(c iris.Context) {
	items, err := a.store.ListTestStories(c.Request().Context(), c.URLParam("project_id"), c.URLParam("q"))
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) createTestStory(c iris.Context) {
	c.SetMaxRequestBodySize(1 << 20)
	var story testlab.Story
	if err := c.ReadJSON(&story); err != nil {
		a.badRequest(c, "invalid story")
		return
	}
	if story.Slug == "" {
		story.Slug = testlab.Slugify(story.Title)
	}
	created, err := a.store.CreateTestStory(c.Request().Context(), story)
	if err != nil {
		a.testLabError(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "test_story.create", "test_story", created.ID, &created.ProjectID, "ok", nil)
	c.StatusCode(http.StatusCreated)
	_ = c.JSON(envelope{Data: created})
}

func (a *API) importTestStories(c iris.Context) {
	c.SetMaxRequestBodySize(8 << 20)
	var in struct {
		ProjectID string `json:"project_id"`
		Defaults  struct {
			Mode        string `json:"mode"`
			Environment string `json:"environment"`
			Platform    string `json:"platform"`
		} `json:"defaults"`
		Stories []struct {
			Filename string `json:"filename"`
			Content  string `json:"content"`
		} `json:"stories"`
	}
	if err := c.ReadJSON(&in); err != nil || strings.TrimSpace(in.ProjectID) == "" || len(in.Stories) == 0 || len(in.Stories) > 50 {
		a.badRequest(c, "project_id and 1..50 stories are required")
		return
	}
	type result struct {
		Slug   string         `json:"slug"`
		Action string         `json:"action"`
		Story  *testlab.Story `json:"story,omitempty"`
		Error  string         `json:"error,omitempty"`
	}
	out := make([]result, 0, len(in.Stories))
	for _, file := range in.Stories {
		if len(file.Content) > testlab.MaxStoryBodyBytes {
			out = append(out, result{filenameSlug(file.Filename), "rejected", nil, "story exceeds 512 KiB"})
			continue
		}
		parsed := testlab.ParseStoryMarkdown(file.Filename, file.Content)
		if in.Defaults.Mode != "" {
			parsed.Mode = in.Defaults.Mode
		}
		if in.Defaults.Environment != "" {
			parsed.Environment = in.Defaults.Environment
		}
		if in.Defaults.Platform != "" {
			parsed.Platform = in.Defaults.Platform
		}
		story := testlab.Story{ProjectID: in.ProjectID, Slug: parsed.Slug, Title: parsed.Title, Body: parsed.Body,
			Mode: parsed.Mode, Environment: parsed.Environment, Platform: parsed.Platform,
			RequiredChecks: parsed.RequiredChecks, Tags: parsed.Tags, Events: parsed.Events,
			TimeoutSeconds: parsed.TimeoutSeconds}
		saved, err := a.store.UpsertImportedTestStory(c.Request().Context(), story)
		if err != nil {
			out = append(out, result{Slug: parsed.Slug, Action: "rejected", Error: err.Error()})
			continue
		}
		out = append(out, result{Slug: saved.Slug, Action: "imported", Story: saved})
	}
	c.StatusCode(http.StatusCreated)
	_ = c.JSON(envelope{Data: iris.Map{"items": out}})
}

func (a *API) getTestStory(c iris.Context) {
	story, err := a.store.GetTestStory(c.Request().Context(), c.Params().Get("id"))
	if err != nil {
		a.testLabError(c, err)
		return
	}
	_ = c.JSON(envelope{Data: story})
}

func (a *API) updateTestStory(c iris.Context) {
	c.SetMaxRequestBodySize(1 << 20)
	current, err := a.store.GetTestStory(c.Request().Context(), c.Params().Get("id"))
	if err != nil {
		a.testLabError(c, err)
		return
	}
	var patch testlab.Story
	if err := c.ReadJSON(&patch); err != nil {
		a.badRequest(c, "invalid story")
		return
	}
	merged := *current
	if patch.Slug != "" {
		merged.Slug = patch.Slug
	}
	if patch.Title != "" {
		merged.Title = patch.Title
	}
	if patch.Body != "" {
		merged.Body = patch.Body
	}
	if patch.Mode != "" {
		merged.Mode = patch.Mode
	}
	if patch.Environment != "" {
		merged.Environment = patch.Environment
	}
	if patch.Platform != "" {
		merged.Platform = patch.Platform
	}
	if patch.RequiredChecks != nil {
		merged.RequiredChecks = patch.RequiredChecks
	}
	if patch.Tags != nil {
		merged.Tags = patch.Tags
	}
	if patch.Events != nil {
		merged.Events = patch.Events
		if len(patch.Events) > 0 {
			merged.RequiredChecks = mergeCheckIDs(merged.RequiredChecks, eventMethods(patch.Events))
		}
	}
	if patch.TimeoutSeconds != 0 {
		merged.TimeoutSeconds = patch.TimeoutSeconds
	}
	updated, err := a.store.UpdateTestStory(c.Request().Context(), merged)
	if err != nil {
		a.testLabError(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "test_story.update", "test_story", updated.ID, &updated.ProjectID, "ok", nil)
	_ = c.JSON(envelope{Data: updated})
}

func (a *API) deleteTestStory(c iris.Context) {
	if err := a.store.DeleteTestStory(c.Request().Context(), c.Params().Get("id")); err != nil {
		a.testLabError(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "test_story.delete", "test_story", c.Params().Get("id"), nil, "ok", nil)
	c.StatusCode(http.StatusNoContent)
}

func (a *API) runTestStory(c iris.Context) {
	c.SetMaxRequestBodySize(1 << 20)
	story, err := a.store.GetTestStory(c.Request().Context(), c.Params().Get("id"))
	if err != nil {
		a.testLabError(c, err)
		return
	}
	var in struct {
		Environment    string              `json:"environment"`
		Platform       string              `json:"platform"`
		TimeoutSeconds int                 `json:"timeout_seconds"`
		Engine         *testlab.EngineSpec `json:"engine"`
		Inputs         map[string]string   `json:"inputs"`
		EventIDs       []string            `json:"event_ids"`
	}
	if c.Request().ContentLength != 0 {
		if err := c.ReadJSON(&in); err != nil {
			a.badRequest(c, "invalid run request")
			return
		}
	}
	spec, err := buildStorySessionSpec(story, storyRunInput{
		Environment: in.Environment, Platform: in.Platform, TimeoutSeconds: in.TimeoutSeconds,
		Engine: in.Engine, Inputs: in.Inputs, EventIDs: in.EventIDs,
	})
	if err != nil {
		a.testLabError(c, err)
		return
	}
	id, err := newSessionID()
	if err != nil {
		a.fail(c, err)
		return
	}
	token, digest, err := newTestToken()
	if err != nil {
		a.fail(c, err)
		return
	}
	session, err := a.store.CreateTestSession(c.Request().Context(), id, spec, digest)
	if err != nil {
		a.testLabError(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "test_story.run", "test_session", session.ID, &story.ProjectID, "queued", nil)
	c.Header("Cache-Control", "no-store")
	c.StatusCode(http.StatusCreated)
	_ = c.JSON(envelope{Data: iris.Map{"session": session, "token": token}})
}

// runTestStoriesBatch queues many stories in one request. Each story keeps its
// own snapshot and events; failures are reported per story instead of failing
// the whole batch.
func (a *API) runTestStoriesBatch(c iris.Context) {
	c.SetMaxRequestBodySize(1 << 20)
	var in struct {
		StoryIDs       []string `json:"story_ids"`
		Environment    string   `json:"environment"`
		Platform       string   `json:"platform"`
		TimeoutSeconds int      `json:"timeout_seconds"`
	}
	if err := c.ReadJSON(&in); err != nil || len(in.StoryIDs) == 0 || len(in.StoryIDs) > 50 {
		a.badRequest(c, "story_ids with 1..50 entries is required")
		return
	}
	seen := map[string]bool{}
	items := make([]iris.Map, 0, len(in.StoryIDs))
	queued := 0
	for _, raw := range in.StoryIDs {
		storyID := strings.TrimSpace(raw)
		if storyID == "" || seen[storyID] {
			continue
		}
		seen[storyID] = true
		story, err := a.store.GetTestStory(c.Request().Context(), storyID)
		if err != nil {
			items = append(items, iris.Map{"story_id": storyID, "error": err.Error()})
			continue
		}
		spec, err := buildStorySessionSpec(story, storyRunInput{
			Environment: in.Environment, Platform: in.Platform, TimeoutSeconds: in.TimeoutSeconds,
		})
		if err != nil {
			items = append(items, iris.Map{"story_id": storyID, "error": err.Error()})
			continue
		}
		sessionID, err := newSessionID()
		if err != nil {
			items = append(items, iris.Map{"story_id": storyID, "error": err.Error()})
			continue
		}
		token, digest, err := newTestToken()
		if err != nil {
			items = append(items, iris.Map{"story_id": storyID, "error": err.Error()})
			continue
		}
		session, err := a.store.CreateTestSession(c.Request().Context(), sessionID, spec, digest)
		if err != nil {
			items = append(items, iris.Map{"story_id": storyID, "error": err.Error()})
			continue
		}
		a.audit(c.Request().Context(), c, "test_story.run_batch", "test_session", session.ID, &story.ProjectID, "queued", nil)
		queued++
		items = append(items, iris.Map{"story_id": storyID, "session": session, "token": token})
	}
	c.Header("Cache-Control", "no-store")
	c.StatusCode(http.StatusCreated)
	_ = c.JSON(envelope{Data: iris.Map{"items": items, "queued": queued}})
}

func filenameSlug(name string) string {
	base := name
	if index := strings.LastIndexAny(base, "\\/"); index >= 0 {
		base = base[index+1:]
	}
	if index := strings.LastIndex(base, "."); index > 0 {
		base = base[:index]
	}
	return testlab.Slugify(base)
}

// selectStoryEvents returns every event when ids is empty, otherwise exactly
// the requested subset in story order. Unknown ids are rejected instead of
// silently running nothing.
func selectStoryEvents(events []testlab.StoryEvent, ids []string) ([]testlab.StoryEvent, error) {
	if len(ids) == 0 {
		return events, nil
	}
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return nil, fmt.Errorf("%w: event_ids must not contain empty ids", testlab.ErrInvalid)
		}
		wanted[id] = true
	}
	out := make([]testlab.StoryEvent, 0, len(wanted))
	for _, event := range events {
		if wanted[event.ID] {
			out = append(out, event)
			delete(wanted, event.ID)
		}
	}
	if len(wanted) > 0 {
		missing := make([]string, 0, len(wanted))
		for id := range wanted {
			missing = append(missing, id)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("%w: unknown event ids: %s", testlab.ErrInvalid, strings.Join(missing, ", "))
	}
	return out, nil
}

type storyRunInput struct {
	Environment    string
	Platform       string
	TimeoutSeconds int
	Engine         *testlab.EngineSpec
	Inputs         map[string]string
	EventIDs       []string
}

// buildStorySessionSpec snapshots a story library entry into the immutable
// session spec used by both single and batch triggers.
func buildStorySessionSpec(story *testlab.Story, in storyRunInput) (testlab.SessionSpec, error) {
	events, err := selectStoryEvents(story.Events, in.EventIDs)
	if err != nil {
		return testlab.SessionSpec{}, err
	}
	required := story.RequiredChecks
	storyText := story.Body
	if len(story.Events) > 0 {
		if len(in.EventIDs) > 0 {
			required = eventMethods(events)
			storyText = scopeStoryToEvents(story.Body, events)
		} else {
			required = mergeCheckIDs(required, eventMethods(events))
		}
	}
	spec := testlab.SessionSpec{
		ProjectID: story.ProjectID, Title: "故事：" + story.Title, Kind: story.Mode, Mode: story.Mode,
		Environment: firstNonEmpty(in.Environment, story.Environment), Platform: firstNonEmpty(in.Platform, story.Platform),
		StoryRef: "test-story:" + story.ID, Story: storyText, Inputs: in.Inputs,
		RequiredChecks: required, Events: events, TimeoutSeconds: story.TimeoutSeconds, Engine: in.Engine,
	}
	if in.TimeoutSeconds != 0 {
		spec.TimeoutSeconds = in.TimeoutSeconds
	}
	spec = spec.Normalize()
	if err := spec.Validate(); err != nil {
		return testlab.SessionSpec{}, err
	}
	return spec, nil
}

func eventMethods(events []testlab.StoryEvent) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.Method)
	}
	return out
}

func mergeCheckIDs(base, extra []string) []string {
	seen := make(map[string]bool, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, list := range [][]string{base, extra} {
		for _, id := range list {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// scopeStoryToEvents keeps a partial run honest: the agent still sees the full
// story for context, but only the selected events are in scope. Skipped when
// the note would push the snapshot past the session story limit.
func scopeStoryToEvents(body string, events []testlab.StoryEvent) string {
	var note strings.Builder
	note.WriteString("\n\n# 本次运行范围（SmartPipe 调度）\n")
	note.WriteString("只执行以下事件；未列出的故事步骤仅供理解上下文，不要求执行。\n")
	for _, event := range events {
		fmt.Fprintf(&note, "- %s：%s（method: %s）\n", event.ID, event.Title, event.Method)
	}
	if len(body)+note.Len() > testlab.MaxStoryBytes {
		return body
	}
	return body + note.String()
}
