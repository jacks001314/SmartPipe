package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"smartpipe/backend/internal/store"
)

// schedulerEvent 是转发给调度器的 GitHub 事件。
type schedulerEvent struct {
	Event       string `json:"event"`
	Action      string `json:"action"`
	ProjectID   string `json:"project_id"`
	Repo        string `json:"repo"`
	Issue       int    `json:"issue_number"`
	CommentID   int64  `json:"comment_id"`
	Author      string `json:"author"`
	Association string `json:"association"`
	Body        string `json:"body"`
	Title       string `json:"title"`
	State       string `json:"state"`
}

// resolveProjectID 用仓库全名找到项目。
func resolveProjectID(ctx context.Context, st *store.Store, repoFullName string) string {
	if st == nil || repoFullName == "" {
		return ""
	}
	projects, err := st.ListProjects(ctx)
	if err != nil {
		return ""
	}
	want := strings.ToLower(strings.TrimSpace(repoFullName))
	for i := range projects {
		p := &projects[i]
		for _, candidate := range []string{p.GitHubRepoURL, p.GitHubIssueRepo} {
			if repoMatches(candidate, want) {
				return p.ID
			}
		}
	}
	return ""
}

func repoMatches(candidate, want string) bool {
	c := strings.ToLower(strings.TrimSpace(candidate))
	if c == "" {
		return false
	}
	c = strings.TrimSuffix(c, ".git")
	c = strings.TrimPrefix(c, "https://github.com/")
	c = strings.TrimPrefix(c, "http://github.com/")
	c = strings.TrimPrefix(c, "git@github.com:")
	return c == want
}

// forwardEvent 把事件异步投给调度器；失败只记日志，不影响 webhook 回执。
func (h *WebhookHandler) forwardEvent(ev schedulerEvent) {
	if strings.TrimSpace(h.ForwardURL) == "" || ev.Event == "" {
		return
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			strings.TrimRight(h.ForwardURL, "/")+"/internal/scheduler/github", bytes.NewReader(raw))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if h.ForwardToken != "" {
			req.Header.Set("X-Scheduler-Token", h.ForwardToken)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Printf("[bridge] forward to scheduler failed: %v", err)
			return
		}
		_ = resp.Body.Close()
	}()
}
