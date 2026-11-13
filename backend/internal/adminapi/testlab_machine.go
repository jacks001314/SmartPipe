package adminapi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/kataras/iris/v12"
	"net/http"
	"smartpipe/backend/internal/store"
	"smartpipe/backend/internal/testlab"
	"strings"
)

func (a *API) registerTestMachines(admin iris.Party, app *iris.Application) {
	admin.Post("/test-machines", a.createTestMachine)
	admin.Get("/test-machines", a.listTestMachines)
	admin.Post("/test-machines/{id}/enabled", a.setTestMachineEnabled)
	admin.Post("/test-machines/{id}/recover", a.recoverTestMachine)
	admin.Get("/test-runs/{id}/evidence/{digest}", a.getTestEvidence)
	p := app.Party("/test-runner")
	p.Use(a.testMachineAuth)
	p.Post("/claim", a.claimTestRun)
	p.Post("/heartbeat", a.heartbeatTestRun)
	p.Post("/report", a.machineReportTestRun)
	p.Post("/release", a.releaseTestMachine)
	p.Post("/evidence", a.uploadTestEvidence)
}
func (a *API) createTestMachine(c iris.Context) {
	c.SetMaxRequestBodySize(4096)
	var in struct {
		Platform string `json:"platform"`
	}
	if c.ReadJSON(&in) != nil {
		a.badRequest(c, "invalid machine")
		return
	}
	switch in.Platform {
	case "windows-amd64", "darwin-arm64", "darwin-amd64", "linux-amd64", "linux-arm64":
	default:
		a.badRequest(c, "unsupported platform")
		return
	}
	var secret [32]byte
	var id [16]byte
	if _, err := rand.Read(secret[:]); err != nil {
		a.fail(c, err)
		return
	}
	if _, err := rand.Read(id[:]); err != nil {
		a.fail(c, err)
		return
	}
	token := hex.EncodeToString(secret[:])
	machineID := "machine-" + hex.EncodeToString(id[:])
	if err := a.store.RegisterDesktopMachine(c.Request().Context(), machineID, in.Platform, testlab.TokenDigest(token)); err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "test_machine.create", "test_machine", machineID, nil, "ok", nil)
	c.Header("Cache-Control", "no-store")
	c.StatusCode(http.StatusCreated)
	_ = c.JSON(envelope{Data: iris.Map{"id": machineID, "platform": in.Platform, "token": token}})
}
func (a *API) listTestMachines(c iris.Context) {
	items, err := a.store.ListDesktopMachines(c.Request().Context())
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}
func (a *API) testMachineAuth(c iris.Context) {
	id := c.GetHeader("X-Test-Machine-ID")
	header := c.GetHeader("Authorization")
	if id == "" || !strings.HasPrefix(header, "Bearer ") || len(header) < 39 {
		c.StopWithStatus(http.StatusUnauthorized)
		return
	}
	if a.store.AuthenticateDesktopMachine(c.Request().Context(), id, strings.TrimPrefix(header, "Bearer ")) != nil {
		c.StopWithStatus(http.StatusUnauthorized)
		return
	}
	c.Values().Set("test_machine", id)
	c.Next()
}
func (a *API) claimTestRun(c iris.Context) {
	c.SetMaxRequestBodySize(4096)
	var in struct {
		Scenarios []string `json:"scenarios"`
	}
	if c.ReadJSON(&in) != nil || len(in.Scenarios) == 0 || len(in.Scenarios) > 32 {
		a.badRequest(c, "scenario capabilities required")
		return
	}
	lease, err := a.store.ClaimDesktopRun(c.Request().Context(), c.Values().GetString("test_machine"), in.Scenarios...)
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

type machineAssignment struct {
	RunID      string         `json:"run_id"`
	Generation int64          `json:"generation"`
	Report     testlab.Report `json:"report"`
}

func readAssignment(c iris.Context) (machineAssignment, bool) {
	c.SetMaxRequestBodySize(1 << 20)
	var in machineAssignment
	if c.ReadJSON(&in) != nil || in.RunID == "" || in.Generation < 1 {
		c.StopWithStatus(http.StatusBadRequest)
		return in, false
	}
	return in, true
}
func (a *API) heartbeatTestRun(c iris.Context) {
	in, ok := readAssignment(c)
	if !ok {
		return
	}
	until, err := a.store.HeartbeatDesktopRun(c.Request().Context(), c.Values().GetString("test_machine"), in.RunID, in.Generation)
	if err != nil {
		a.testLabError(c, err)
		return
	}
	_ = c.JSON(envelope{Data: iris.Map{"expires_at": until}})
}
func (a *API) machineReportTestRun(c iris.Context) {
	in, ok := readAssignment(c)
	if !ok {
		return
	}
	run, err := a.store.FinishDesktopRun(c.Request().Context(), c.Values().GetString("test_machine"), in.RunID, in.Generation, in.Report)
	if err != nil {
		a.testLabError(c, err)
		return
	}
	_ = c.JSON(envelope{Data: run})
}
func (a *API) releaseTestMachine(c iris.Context) {
	in, ok := readAssignment(c)
	if !ok {
		return
	}
	err := a.store.ReleaseDesktopMachine(c.Request().Context(), c.Values().GetString("test_machine"), in.RunID, in.Generation)
	if err != nil {
		a.testLabError(c, err)
		return
	}
	c.StatusCode(http.StatusNoContent)
}
