package testlab

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	MaxStoryBodyBytes = 512 << 10
	MaxStoryTags      = 16
	MaxStoryEvents    = 64
)

var storySlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var storyEventIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// StoryEvent is one trigger in a story plus the test method that decides
// whether it worked. The method id is exactly the check id the agent records
// through engine_check_record, which is what the console joins on.
type StoryEvent struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Trigger     string `json:"trigger,omitempty"`
	TriggerKind string `json:"trigger_kind,omitempty"`
	Method      string `json:"method"`
	Expected    string `json:"expected,omitempty"`
}

var storyTriggerKinds = map[string]bool{"": true, "ui": true, "shell": true, "http": true, "manual": true}

// ValidateEvents checks the shared event contract used by stories and session
// snapshots. Method ids are unique because they key the check results.
func ValidateEvents(events []StoryEvent) error {
	if len(events) > MaxStoryEvents {
		return fmt.Errorf("%w: too many story events", ErrInvalid)
	}
	ids := map[string]bool{}
	methods := map[string]bool{}
	for _, event := range events {
		if !storyEventIDPattern.MatchString(event.ID) {
			return fmt.Errorf("%w: event id must match %s", ErrInvalid, storyEventIDPattern)
		}
		if ids[event.ID] {
			return fmt.Errorf("%w: duplicate event id %q", ErrInvalid, event.ID)
		}
		ids[event.ID] = true
		if strings.TrimSpace(event.Title) == "" || len(event.Title) > 200 {
			return fmt.Errorf("%w: event title is required and limited to 200 bytes", ErrInvalid)
		}
		if !validCheckID(event.Method) {
			return fmt.Errorf("%w: invalid event method %q", ErrInvalid, event.Method)
		}
		if methods[event.Method] {
			return fmt.Errorf("%w: duplicate event method %q", ErrInvalid, event.Method)
		}
		methods[event.Method] = true
		if !storyTriggerKinds[event.TriggerKind] {
			return fmt.Errorf("%w: trigger_kind must be ui, shell, http or manual", ErrInvalid)
		}
		if len(event.Trigger) > 4000 || len(event.Expected) > 4000 {
			return fmt.Errorf("%w: event trigger/expected is limited to 4000 bytes", ErrInvalid)
		}
	}
	return nil
}

// Story is an editable Markdown test case owned by a project. It is the
// authoring surface; a run copies its body into an immutable SessionSpec.
type Story struct {
	ID             string       `json:"id"`
	ProjectID      string       `json:"project_id"`
	Slug           string       `json:"slug"`
	Title          string       `json:"title"`
	Body           string       `json:"body"`
	Mode           string       `json:"mode"`
	Environment    string       `json:"environment"`
	Platform       string       `json:"platform"`
	RequiredChecks []string     `json:"required_checks"`
	Tags           []string     `json:"tags"`
	Events         []StoryEvent `json:"events"`
	TimeoutSeconds int          `json:"timeout_seconds"`
	CreatedAt      string       `json:"created_at,omitempty"`
	UpdatedAt      string       `json:"updated_at,omitempty"`
}

func (s Story) Validate() error {
	if strings.TrimSpace(s.ProjectID) == "" {
		return fmt.Errorf("%w: project is required", ErrInvalid)
	}
	if !storySlugPattern.MatchString(s.Slug) {
		return fmt.Errorf("%w: slug must match %s", ErrInvalid, storySlugPattern)
	}
	if strings.TrimSpace(s.Title) == "" || len(s.Title) > 200 {
		return fmt.Errorf("%w: title is required and limited to 200 bytes", ErrInvalid)
	}
	if strings.TrimSpace(s.Body) == "" || len(s.Body) > MaxStoryBodyBytes {
		return fmt.Errorf("%w: body is required and limited to %d bytes", ErrInvalid, MaxStoryBodyBytes)
	}
	if !sessionKinds[s.Mode] {
		return fmt.Errorf("%w: mode must be story, bug, explore, hotswap or gate", ErrInvalid)
	}
	if strings.TrimSpace(s.Environment) == "" || len(s.Environment) > 64 {
		return fmt.Errorf("%w: environment is required", ErrInvalid)
	}
	if strings.TrimSpace(s.Platform) == "" || len(s.Platform) > 64 {
		return fmt.Errorf("%w: platform is required", ErrInvalid)
	}
	if len(s.RequiredChecks) > MaxSessionChecks {
		return fmt.Errorf("%w: too many required checks", ErrInvalid)
	}
	for _, id := range s.RequiredChecks {
		if !validCheckID(id) {
			return fmt.Errorf("%w: invalid required check %q", ErrInvalid, id)
		}
	}
	if len(s.Tags) > MaxStoryTags {
		return fmt.Errorf("%w: too many tags", ErrInvalid)
	}
	for _, tag := range s.Tags {
		if strings.TrimSpace(tag) == "" || len(tag) > 40 {
			return fmt.Errorf("%w: invalid tag", ErrInvalid)
		}
	}
	if err := ValidateEvents(s.Events); err != nil {
		return err
	}
	if s.TimeoutSeconds == 0 {
		s.TimeoutSeconds = 30 * 60
	}
	if s.TimeoutSeconds < MinSessionTimeout || s.TimeoutSeconds > MaxSessionTimeout {
		return fmt.Errorf("%w: timeout out of range", ErrInvalid)
	}
	return nil
}

// ParsedStory is the result of importing a Markdown file.
type ParsedStory struct {
	Slug           string
	Title          string
	Body           string
	Mode           string
	Environment    string
	Platform       string
	RequiredChecks []string
	Tags           []string
	Events         []StoryEvent
	TimeoutSeconds int
}

// ParseStoryMarkdown extracts the small YAML subset used by story files.
// Unknown keys are ignored so authors can keep their own metadata.
func ParseStoryMarkdown(filename, content string) ParsedStory {
	text := strings.TrimPrefix(content, "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	out := ParsedStory{Body: text, Mode: "story", Environment: "syntropy", Platform: "windows-amd64"}
	if strings.HasPrefix(text, "---\n") {
		if end := strings.Index(text[4:], "\n---"); end >= 0 {
			header := text[4 : 4+end]
			rest := text[4+end+4:]
			rest = strings.TrimPrefix(rest, "\n")
			out.Body = rest
			parseStoryHeader(header, &out)
		}
	}
	if out.Title == "" {
		out.Title = strings.TrimSuffix(baseName(filename), fileExt(baseName(filename)))
	}
	if out.Slug == "" {
		out.Slug = Slugify(out.Title)
	}
	for i := range out.Events {
		event := &out.Events[i]
		if event.ID == "" {
			event.ID = Slugify(event.Title)
			if event.ID == "story" {
				event.ID = fmt.Sprintf("event-%d", i+1)
			}
		}
		if event.Method == "" {
			event.Method = event.ID
		}
	}
	if len(out.RequiredChecks) == 0 {
		for _, event := range out.Events {
			out.RequiredChecks = append(out.RequiredChecks, event.Method)
		}
	}
	return out
}

func parseStoryHeader(header string, out *ParsedStory) {
	listKey := ""
	inEvents := false
	for _, raw := range strings.Split(header, "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " \t"))
		if inEvents && indent > 0 {
			if strings.HasPrefix(trimmed, "- ") {
				out.Events = append(out.Events, StoryEvent{})
				parseStoryEventField(trimmed[2:], &out.Events[len(out.Events)-1])
			} else if len(out.Events) > 0 {
				parseStoryEventField(trimmed, &out.Events[len(out.Events)-1])
			}
			continue
		}
		inEvents = false
		if strings.HasPrefix(trimmed, "- ") && listKey != "" {
			value := unquoteYAML(strings.TrimSpace(trimmed[2:]))
			switch listKey {
			case "required_checks":
				out.RequiredChecks = append(out.RequiredChecks, value)
			case "tags":
				out.Tags = append(out.Tags, value)
			}
			continue
		}
		key, value, ok := strings.Cut(raw, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if value == "" {
			listKey = key
			inEvents = key == "events"
			continue
		}
		listKey = ""
		switch key {
		case "id", "slug":
			out.Slug = unquoteYAML(value)
		case "title":
			out.Title = unquoteYAML(value)
		case "mode":
			out.Mode = unquoteYAML(value)
		case "environment":
			out.Environment = unquoteYAML(value)
		case "platform":
			out.Platform = unquoteYAML(value)
		case "required_checks":
			out.RequiredChecks = parseYAMLList(value)
		case "tags":
			out.Tags = parseYAMLList(value)
		case "timeout_seconds":
			if seconds, err := strconv.Atoi(unquoteYAML(value)); err == nil {
				out.TimeoutSeconds = seconds
			}
		}
	}
}

func parseStoryEventField(line string, event *StoryEvent) {
	key, value, ok := strings.Cut(line, ":")
	if !ok {
		return
	}
	key = strings.TrimSpace(key)
	value = unquoteYAML(strings.TrimSpace(value))
	switch key {
	case "id":
		event.ID = value
	case "title":
		event.Title = value
	case "trigger":
		event.Trigger = value
	case "trigger_kind", "kind":
		event.TriggerKind = value
	case "method", "check":
		event.Method = value
	case "expected":
		event.Expected = value
	}
}

func parseYAMLList(value string) []string {
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		value = value[1 : len(value)-1]
	}
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = unquoteYAML(strings.TrimSpace(item)); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func unquoteYAML(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
		return value[1 : len(value)-1]
	}
	return value
}

var slugNoise = regexp.MustCompile(`[^a-z0-9]+`)

func Slugify(value string) string {
	slug := slugNoise.ReplaceAllString(strings.ToLower(strings.TrimSpace(value)), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "story"
	}
	if len(slug) > 64 {
		slug = strings.Trim(slug[:64], "-")
	}
	return slug
}

func baseName(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	if index := strings.LastIndex(path, "/"); index >= 0 {
		return path[index+1:]
	}
	return path
}

func fileExt(name string) string {
	if index := strings.LastIndex(name, "."); index > 0 {
		return name[index:]
	}
	return ""
}
