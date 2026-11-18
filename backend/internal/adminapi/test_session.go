package adminapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/kataras/iris/v12"
	"net/http"
	"smartpipe/backend/internal/store"
	"smartpipe/backend/internal/testlab"
	"strconv"
	"strings"
	"time"
)

// Test sessions are the agent-facing counterpart of deterministic test runs.
// The runner holds the machine lease and executes bounded commands; the agent
// reads the story, observes the machine, records checks and asks for a verdict.
func (a *API) registerTestSessions(admin iris.Party, app *iris.Application) {
	admin.Get("/test-sessions", a.listTestSessions)
	admin.Post("/test-sessions", a.createTestSession)
	admin.Post("/test-sessions/batch", a.createTestSessionBatch)
	admin.Get("/test-sessions/{id}", a.getTestSession)
	admin.Get("/test-sessions/{id}/commands", a.listTestSessionCommands)
	admin.Get("/test-sessions/{id}/evidence/{digest}", a.getTestSessionEvidence)
	admin.Post("/test-sessions/{id}/cancel", a.cancelTestSession)

	agent := app.Party("/test-session")
	agent.Use(a.testSessionAuth)
	agent.Get("/{id}", a.agentGetTestSession)
	agent.Post("/{id}/command", a.agentTestSessionCommand)
	agent.Get("/{id}/command/{cid}", a.agentTestSessionCommandResult)
	agent.Post("/{id}/check", a.agentTestSessionCheck)
	agent.Post("/{id}/evidence", a.agentTestSessionEvidence)
	agent.Get("/{id}/evidence/{digest}", a.agentGetTestSessionEvidence)
	agent.Post("/{id}/finish", a.agentTestSessionFinish)

	machine := app.Party("/test-runner/session")
	machine.Use(a.testMachineAuth)
	machine.Post("/claim", a.claimTestSession)
	machine.Post("/heartbeat", a.heartbeatTestSession)
	machine.Post("/next", a.nextTestSessionCommand)
	machine.Post("/result", a.completeTestSessionCommand)
	machine.Post("/evidence", a.uploadTestSessionEvidence)
	machine.Post("/report", a.reportTestSession)
	machine.Post("/release", a.releaseTestSession)
}

func newTestToken() (string, string, error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", "", err
	}
	token := hex.EncodeToString(secret[:])
	return token, testlab.TokenDigest(token), nil
}

func newSessionID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return "session-" + hex.EncodeToString(id[:]), nil
}

func (a *API) listTestSessions(c iris.Context) {
	items, err := a.store.ListTestSessions(c.Request().Context(), c.URLParam("project_id"))
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) createTestSession(c iris.Context) {
	c.SetMaxRequestBodySize(1 << 20)
	var spec testlab.SessionSpec
	if err := c.ReadJSON(&spec); err != nil {
		a.badRequest(c, "invalid test session spec")
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
	a.audit(c.Request().Context(), c, "test_session.create", "test_session", session.ID, &session.Spec.ProjectID, "ok", nil)
	c.Header("Cache-Control", "no-store")
	c.StatusCode(http.StatusCreated)
	_ = c.JSON(envelope{Data: iris.Map{"session": session, "token": token}})
}

func (a *API) createTestSessionBatch(c iris.Context) {
	c.SetMaxRequestBodySize(1 << 20)
	var in struct {
		Sessions []testlab.SessionSpec `json:"sessions"`
	}
	if err := c.ReadJSON(&in); err != nil || len(in.Sessions) == 0 || len(in.Sessions) > 50 {
		a.badRequest(c, "sessions must contain 1..50 specs")
		return
	}
	type created struct {
		Session *testlab.Session `json:"session"`
		Token   string           `json:"token"`
	}
	out := make([]created, 0, len(in.Sessions))
	for _, spec := range in.Sessions {
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
		out = append(out, created{Session: session, Token: token})
	}
	c.Header("Cache-Control", "no-store")
	c.StatusCode(http.StatusCreated)
	_ = c.JSON(envelope{Data: out})
}

func (a *API) getTestSession(c iris.Context) {
	session, err := a.store.GetTestSession(c.Request().Context(), c.Params().Get("id"))
	if err != nil {
		a.testLabError(c, err)
		return
	}
	_ = c.JSON(envelope{Data: session})
}

func (a *API) listTestSessionCommands(c iris.Context) {
	items, err := a.store.ListTestSessionCommands(c.Request().Context(), c.Params().Get("id"))
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) getTestSessionEvidence(c iris.Context) {
	digest := strings.TrimPrefix(c.Params().Get("digest"), "sha256:")
	item, err := a.store.GetTestSessionEvidence(c.Request().Context(), c.Params().Get("id"), digest)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.StatusCode(http.StatusNotFound)
			return
		}
		a.fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Type", item.MIMEType)
	_, _ = c.Write(item.Payload)
}

func (a *API) cancelTestSession(c iris.Context) {
	if err := a.store.CancelTestSession(c.Request().Context(), c.Params().Get("id")); err != nil {
		a.testLabError(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "test_session.cancel", "test_session", c.Params().Get("id"), nil, "ok", nil)
	a.getTestSession(c)
}

func (a *API) testSessionAuth(c iris.Context) {
	id := c.Params().Get("id")
	header := c.GetHeader("Authorization")
	if id == "" || !strings.HasPrefix(header, "Bearer ") || len(header) < 39 {
		c.StopWithStatus(http.StatusUnauthorized)
		return
	}
	if a.store.AuthenticateTestSession(c.Request().Context(), id, strings.TrimPrefix(header, "Bearer ")) != nil {
		c.StopWithStatus(http.StatusUnauthorized)
		return
	}
	c.Next()
}

func (a *API) agentGetTestSession(c iris.Context) {
	a.getTestSession(c)
}

func (a *API) agentTestSessionCommand(c iris.Context) {
	c.SetMaxRequestBodySize(256 << 10)
	var in struct {
		Kind string          `json:"kind"`
		Args json.RawMessage `json:"args"`
	}
	if err := c.ReadJSON(&in); err != nil {
		a.badRequest(c, "invalid command")
		return
	}
	command, err := a.store.EnqueueTestSessionCommand(c.Request().Context(), c.Params().Get("id"), in.Kind, in.Args)
	if err != nil {
		a.testLabError(c, err)
		return
	}
	c.StatusCode(http.StatusAccepted)
	_ = c.JSON(envelope{Data: command})
}

func (a *API) agentTestSessionCommandResult(c iris.Context) {
	wait := 25 * time.Second
	if raw := c.URLParam("wait_ms"); raw != "" {
		if ms, err := strconv.Atoi(raw); err == nil && ms >= 0 && ms <= 30000 {
			wait = time.Duration(ms) * time.Millisecond
		}
	}
	deadline := time.Now().Add(wait)
	for {
		command, err := a.store.GetTestSessionCommand(c.Request().Context(), c.Params().Get("id"), c.Params().Get("cid"))
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				c.StopWithStatus(http.StatusNotFound)
				return
			}
			a.fail(c, err)
			return
		}
		if command.Status != "PENDING" && command.Status != "RUNNING" {
			_ = c.JSON(envelope{Data: command})
			return
		}
		if time.Now().After(deadline) {
			c.StatusCode(http.StatusAccepted)
			_ = c.JSON(envelope{Data: command})
			return
		}
		select {
		case <-c.Request().Context().Done():
			return
		case <-time.After(400 * time.Millisecond):
		}
	}
}

func (a *API) agentTestSessionCheck(c iris.Context) {
	c.SetMaxRequestBodySize(64 << 10)
	var in struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Evidence string `json:"evidence"`
		Note     string `json:"note"`
	}
	if err := c.ReadJSON(&in); err != nil || len(in.Note) > 4000 {
		a.badRequest(c, "invalid check")
		return
	}
	err := a.store.RecordTestSessionCheck(c.Request().Context(), c.Params().Get("id"),
		testlab.SessionCheck{ID: in.ID, Status: in.Status, Evidence: in.Evidence, Note: in.Note})
	if err != nil {
		a.testLabError(c, err)
		return
	}
	a.getTestSession(c)
}

func (a *API) agentTestSessionEvidence(c iris.Context) {
	c.SetMaxRequestBodySize(12 << 20)
	var in struct {
		SHA256   string `json:"sha256"`
		MIMEType string `json:"mime_type"`
		Payload  []byte `json:"payload"`
	}
	if err := c.ReadJSON(&in); err != nil {
		a.badRequest(c, "invalid evidence")
		return
	}
	ref, err := a.store.PutTestSessionAgentEvidence(c.Request().Context(), c.Params().Get("id"),
		store.DesktopEvidence{SHA256: in.SHA256, MIMEType: in.MIMEType, Payload: in.Payload})
	if err != nil {
		a.testLabError(c, err)
		return
	}
	_ = c.JSON(envelope{Data: iris.Map{"reference": ref}})
}

func (a *API) agentGetTestSessionEvidence(c iris.Context) {
	digest := strings.TrimPrefix(c.Params().Get("digest"), "sha256:")
	item, err := a.store.GetTestSessionEvidence(c.Request().Context(), c.Params().Get("id"), digest)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.StatusCode(http.StatusNotFound)
			return
		}
		a.fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Type", item.MIMEType)
	_, _ = c.Write(item.Payload)
}

func (a *API) agentTestSessionFinish(c iris.Context) {
	c.SetMaxRequestBodySize(64 << 10)
	var in struct {
		Verdict string `json:"verdict"`
		Summary string `json:"summary"`
	}
	if err := c.ReadJSON(&in); err != nil {
		a.badRequest(c, "invalid finish request")
		return
	}
	if err := a.store.RequestTestSessionFinish(c.Request().Context(), c.Params().Get("id"), in.Verdict, in.Summary); err != nil {
		a.testLabError(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "test_session.finish_request", "test_session", c.Params().Get("id"), nil, in.Verdict, nil)
	c.StatusCode(http.StatusAccepted)
	_ = c.JSON(envelope{Data: iris.Map{"status": "finishing"}})
}

func (a *API) claimTestSession(c iris.Context) {
	c.SetMaxRequestBodySize(4096)
	var in struct {
		Environments []string `json:"environments"`
	}
	if c.ReadJSON(&in) != nil || len(in.Environments) > 16 {
		a.badRequest(c, "environments required")
		return
	}
	lease, err := a.store.ClaimTestSession(c.Request().Context(), c.Values().GetString("test_machine"), in.Environments...)
	if errors.Is(err, store.ErrNotFound) {
		c.StatusCode(http.StatusNoContent)
		return
	}
	if err != nil {
		a.testLabError(c, err)
		return
	}
	_ = c.JSON(envelope{Data: lease})
}

type sessionAssignment struct {
	SessionID  string `json:"session_id"`
	Generation int64  `json:"generation"`
}

func readSessionAssignment(c iris.Context) (sessionAssignment, bool) {
	c.SetMaxRequestBodySize(1 << 20)
	var in sessionAssignment
	if c.ReadJSON(&in) != nil || in.SessionID == "" || in.Generation < 1 {
		c.StopWithStatus(http.StatusBadRequest)
		return in, false
	}
	return in, true
}

func (a *API) heartbeatTestSession(c iris.Context) {
	in, ok := readSessionAssignment(c)
	if !ok {
		return
	}
	until, deadline, err := a.store.HeartbeatTestSession(c.Request().Context(), c.Values().GetString("test_machine"), in.SessionID, in.Generation)
	if err != nil {
		a.testLabError(c, err)
		return
	}
	_ = c.JSON(envelope{Data: iris.Map{"expires_at": until, "deadline": deadline}})
}

func (a *API) nextTestSessionCommand(c iris.Context) {
	c.SetMaxRequestBodySize(4096)
	var in struct {
		SessionID   string `json:"session_id"`
		Generation  int64  `json:"generation"`
		WaitSeconds int    `json:"wait_seconds"`
	}
	if c.ReadJSON(&in) != nil || in.SessionID == "" || in.Generation < 1 {
		c.StopWithStatus(http.StatusBadRequest)
		return
	}
	if in.WaitSeconds < 0 || in.WaitSeconds > 30 {
		in.WaitSeconds = 25
	}
	deadline := time.Now().Add(time.Duration(in.WaitSeconds) * time.Second)
	for {
		command, err := a.store.TakeTestSessionCommand(c.Request().Context(), c.Values().GetString("test_machine"), in.SessionID, in.Generation)
		if err == nil {
			_ = c.JSON(envelope{Data: command})
			return
		}
		if errors.Is(err, store.ErrSessionTerminal) {
			c.StatusCode(http.StatusConflict)
			_ = c.JSON(envelope{Err: &apiError{Code: "test_session_terminal", Message: "session is no longer running"}})
			return
		}
		if !errors.Is(err, store.ErrNotFound) {
			a.testLabError(c, err)
			return
		}
		if time.Now().After(deadline) {
			c.StatusCode(http.StatusNoContent)
			return
		}
		select {
		case <-c.Request().Context().Done():
			return
		case <-time.After(400 * time.Millisecond):
		}
	}
}

func (a *API) completeTestSessionCommand(c iris.Context) {
	c.SetMaxRequestBodySize(1 << 20)
	var in struct {
		SessionID  string          `json:"session_id"`
		Generation int64           `json:"generation"`
		CommandID  string          `json:"command_id"`
		Status     string          `json:"status"`
		Result     json.RawMessage `json:"result"`
	}
	if c.ReadJSON(&in) != nil || in.SessionID == "" || in.CommandID == "" || in.Generation < 1 {
		c.StopWithStatus(http.StatusBadRequest)
		return
	}
	if err := a.store.CompleteTestSessionCommand(c.Request().Context(), c.Values().GetString("test_machine"), in.SessionID, in.Generation, in.CommandID, in.Status, in.Result); err != nil {
		a.testLabError(c, err)
		return
	}
	c.StatusCode(http.StatusNoContent)
}

func (a *API) uploadTestSessionEvidence(c iris.Context) {
	c.SetMaxRequestBodySize(12 << 20)
	var in struct {
		SessionID  string `json:"session_id"`
		Generation int64  `json:"generation"`
		Evidence   struct {
			SHA256   string `json:"sha256"`
			MIMEType string `json:"mime_type"`
			Payload  []byte `json:"payload"`
		} `json:"evidence"`
	}
	if c.ReadJSON(&in) != nil || in.SessionID == "" || in.Generation < 1 {
		c.StopWithStatus(http.StatusBadRequest)
		return
	}
	ref, err := a.store.PutTestSessionMachineEvidence(c.Request().Context(), c.Values().GetString("test_machine"), in.SessionID, in.Generation,
		store.DesktopEvidence{SHA256: in.Evidence.SHA256, MIMEType: in.Evidence.MIMEType, Payload: in.Evidence.Payload})
	if err != nil {
		a.testLabError(c, err)
		return
	}
	_ = c.JSON(envelope{Data: iris.Map{"reference": ref}})
}

func (a *API) reportTestSession(c iris.Context) {
	c.SetMaxRequestBodySize(64 << 10)
	var in struct {
		SessionID  string `json:"session_id"`
		Generation int64  `json:"generation"`
		Verdict    string `json:"verdict"`
		Summary    string `json:"summary"`
	}
	if c.ReadJSON(&in) != nil || in.SessionID == "" || in.Generation < 1 {
		c.StopWithStatus(http.StatusBadRequest)
		return
	}
	session, err := a.store.FinishTestSession(c.Request().Context(), c.Values().GetString("test_machine"), in.SessionID, in.Generation, in.Verdict, in.Summary)
	if err != nil {
		a.testLabError(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "test_session.report", "test_session", session.ID, &session.Spec.ProjectID, session.Verdict, nil)
	_ = c.JSON(envelope{Data: session})
}

func (a *API) releaseTestSession(c iris.Context) {
	in, ok := readSessionAssignment(c)
	if !ok {
		return
	}
	if err := a.store.ReleaseTestSession(c.Request().Context(), c.Values().GetString("test_machine"), in.SessionID, in.Generation); err != nil {
		a.testLabError(c, err)
		return
	}
	c.StatusCode(http.StatusNoContent)
}
