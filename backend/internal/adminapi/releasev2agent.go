package adminapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/releaseagent"
	"smartpipe/backend/internal/releasev2"
	"smartpipe/backend/internal/store"
)

func (a *API) registerReleaseV2Agent(p iris.Party) {
	p.Post("/v2/runs/{id}/agent", a.startReleaseV2Agent)
	p.Get("/v2/runs/{id}/agent", a.getReleaseV2Agent)
}

type releaseV2AgentRequest struct {
	Scopes []string `json:"scopes"`
	Model  string   `json:"model"`
	Effort string   `json:"effort"`
}

func (a *API) startReleaseV2Agent(c iris.Context) {
	if a.releaseAgent == nil || !a.releaseAgent.Enabled() {
		c.StatusCode(http.StatusServiceUnavailable)
		_ = c.JSON(envelope{Err: &apiError{Code: "agent_disabled", Message: "服务端未开启发布 Agent（SMARTPIPE_RELEASE_AGENT=off）"}})
		return
	}
	var in releaseV2AgentRequest
	_ = decodeBody(c, &in)
	ctx := c.Request().Context()
	runID := c.Params().Get("id")
	bundle, err := a.releaseV2.GetRunBundle(ctx, runID, 0, 1)
	if err != nil {
		a.releaseV2Error(c, err)
		return
	}
	// 未显式指定模型时，沿用项目配置的模型与推理强度。
	if in.Model == "" || in.Effort == "" {
		if project, perr := a.store.GetProject(ctx, bundle.Run.ProjectID); perr == nil {
			if in.Model == "" {
				in.Model = project.CodexModel
			}
			if in.Effort == "" {
				in.Effort = project.CodexReasoningEffort
			}
		}
	}
	session, err := a.releaseAgent.Launch(ctx, bundle, releaseagent.Options{
		Scopes: in.Scopes, Model: strings.TrimSpace(in.Model), Effort: strings.TrimSpace(in.Effort), Actor: a.actor(c),
	})
	if err != nil {
		switch {
		case errors.Is(err, releaseagent.ErrAlreadyRunning):
			c.StatusCode(http.StatusConflict)
			_ = c.JSON(envelope{Err: &apiError{Code: "agent_running", Message: "该运行已有发布 Agent 在跑"}})
		case errors.Is(err, releaseagent.ErrLimit):
			c.StatusCode(http.StatusConflict)
			_ = c.JSON(envelope{Err: &apiError{Code: "agent_limit", Message: "发布 Agent 并发会话已达上限，请等待现有会话结束后重试"}})
		case errors.Is(err, releasev2.ErrInvalid):
			c.StatusCode(http.StatusBadRequest)
			_ = c.JSON(envelope{Err: &apiError{Code: "invalid_argument", Message: err.Error()}})
		default:
			a.releaseV2Error(c, err)
		}
		return
	}
	pid := bundle.Run.ProjectID
	a.audit(ctx, c, "release.v2.agent.start", "release_run_v2", runID, &pid, "ok", map[string]any{
		"session_id": session.ID, "model": session.Model, "scopes": in.Scopes,
	})
	c.StatusCode(http.StatusAccepted)
	_ = c.JSON(envelope{Data: session})
}

func (a *API) getReleaseV2Agent(c iris.Context) {
	runID := c.Params().Get("id")
	if _, err := a.releaseV2.GetRun(c.Request().Context(), runID); err != nil {
		a.releaseV2Error(c, err)
		return
	}
	out := iris.Map{"enabled": a.releaseAgent != nil && a.releaseAgent.Enabled(), "running": a.releaseAgent.Running(runID)}
	session, err := a.store.LatestAgentSession(c.Request().Context(), runID)
	if err == nil {
		out["session"] = session
	} else if !errors.Is(err, store.ErrNotFound) {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: out})
}

// ---- 事件流（SSE） ----
//
// EventSource 不能带 Authorization 头，所以控制台先用一次普通请求换一张
// 短期票据，再用票据订阅事件流；票据只绑定这一个运行、60 秒内有效。

type eventTicket struct {
	runID     string
	expiresAt time.Time
}

func (a *API) issueReleaseV2EventTicket(c iris.Context) {
	runID := c.Params().Get("id")
	if _, err := a.releaseV2.GetRun(c.Request().Context(), runID); err != nil {
		a.releaseV2Error(c, err)
		return
	}
	ticket := "rev_" + strings.TrimPrefix(releasev2.NewID("t"), "t_")
	expires := time.Now().Add(60 * time.Second)
	a.eventTicketMu.Lock()
	if a.eventTickets == nil {
		a.eventTickets = map[string]eventTicket{}
	}
	for key, item := range a.eventTickets {
		if time.Now().After(item.expiresAt) {
			delete(a.eventTickets, key)
		}
	}
	a.eventTickets[ticket] = eventTicket{runID: runID, expiresAt: expires}
	a.eventTicketMu.Unlock()
	_ = c.JSON(envelope{Data: iris.Map{"ticket": ticket, "expires_at": expires}})
}

func (a *API) consumeEventTicket(ticket, runID string) bool {
	a.eventTicketMu.Lock()
	defer a.eventTicketMu.Unlock()
	item, ok := a.eventTickets[ticket]
	if !ok || item.runID != runID || time.Now().After(item.expiresAt) {
		return false
	}
	delete(a.eventTickets, ticket)
	return true
}

// streamReleaseV2Events 用 SSE 推送运行事件：先补发游标之后的存量，再按秒轮询追加。
// 单条连接最多保持 5 分钟，前端断开或到期后自行重连（会带最新游标）。
func (a *API) streamReleaseV2Events(c iris.Context) {
	runID := c.Params().Get("id")
	if !a.consumeEventTicket(c.URLParam("ticket"), runID) {
		c.StatusCode(http.StatusUnauthorized)
		_, _ = c.WriteString("invalid or expired event ticket")
		return
	}
	after, _ := strconv.ParseInt(c.URLParam("after"), 10, 64)
	w := c.ResponseWriter()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	ctx := c.Request().Context()
	cursor := after
	deadline := time.Now().Add(5 * time.Minute)
	lastPing := time.Now()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		events, err := a.store.ListEvents(ctx, runID, cursor, 200)
		if err == nil {
			for _, ev := range events {
				payload, merr := json.Marshal(ev)
				if merr != nil {
					continue
				}
				fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.ID, payload)
				cursor = ev.ID
			}
		}
		if flusher != nil {
			flusher.Flush()
		}
		if time.Since(lastPing) > 15*time.Second {
			fmt.Fprint(w, ": ping\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			lastPing = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if time.Now().After(deadline) {
				return
			}
		}
	}
}
