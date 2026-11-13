package adminapi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/kataras/iris/v12"
	"net/http"
	"smartpipe/backend/internal/testlab"
)

// These initial endpoints are admin-only. Do not provision an admin token on
// desktop runners: machine-scoped authentication and leasing are a separate step.
func (a *API) registerTestLab(p iris.Party) {
	p.Get("/test-runs", a.listDesktopTestRuns)
	p.Get("/projects/{id}/test-gate", a.getDesktopGate)
	p.Post("/projects/{id}/test-gate", a.setDesktopGate)
	p.Post("/test-runs", a.createDesktopTestRun)
	p.Get("/test-runs/{id}", a.getDesktopTestRun)
	p.Post("/test-runs/{id}/start", a.startDesktopTestRun)
	p.Post("/test-runs/{id}/cancel", a.cancelDesktopTestRun)
	p.Post("/test-runs/{id}/report", a.reportDesktopTestRun)
}

func (a *API) listDesktopTestRuns(c iris.Context) {
	runs, err := a.store.ListDesktopTestRuns(c.Request().Context(), c.URLParam("project_id"))
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: runs})
}
func (a *API) testLabError(c iris.Context, err error) {
	if errors.Is(err, testlab.ErrInvalid) {
		a.badRequest(c, err.Error())
		return
	}
	if errors.Is(err, testlab.ErrState) {
		c.StatusCode(http.StatusConflict)
		_ = c.JSON(envelope{Err: &apiError{Code: "test_run_state", Message: err.Error()}})
		return
	}
	a.fail(c, err)
}
func (a *API) createDesktopTestRun(c iris.Context) {
	c.SetMaxRequestBodySize(1 << 20)
	var spec testlab.Spec
	if err := c.ReadJSON(&spec); err != nil {
		a.badRequest(c, "invalid test spec")
		return
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		a.fail(c, err)
		return
	}
	run, err := a.store.CreateDesktopTestRun(c.Request().Context(), "test-"+hex.EncodeToString(id[:]), spec)
	if err != nil {
		a.testLabError(c, err)
		return
	}
	c.StatusCode(http.StatusCreated)
	a.audit(c.Request().Context(), c, "test_run.create", "test_run", run.ID, &run.Spec.ProjectID, "ok", nil)
	_ = c.JSON(envelope{Data: run})
}
func (a *API) getDesktopTestRun(c iris.Context) {
	run, err := a.store.GetDesktopTestRun(c.Request().Context(), c.Params().Get("id"))
	if err != nil {
		a.testLabError(c, err)
		return
	}
	_ = c.JSON(envelope{Data: run})
}
func (a *API) startDesktopTestRun(c iris.Context) {
	if err := a.store.StartDesktopTestRun(c.Request().Context(), c.Params().Get("id")); err != nil {
		a.testLabError(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "test_run.start", "test_run", c.Params().Get("id"), nil, "ok", nil)
	a.getDesktopTestRun(c)
}
func (a *API) cancelDesktopTestRun(c iris.Context) {
	if err := a.store.CancelDesktopTestRun(c.Request().Context(), c.Params().Get("id")); err != nil {
		a.testLabError(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "test_run.cancel", "test_run", c.Params().Get("id"), nil, "ok", nil)
	a.getDesktopTestRun(c)
}
func (a *API) reportDesktopTestRun(c iris.Context) {
	c.SetMaxRequestBodySize(1 << 20)
	var report testlab.Report
	if err := c.ReadJSON(&report); err != nil {
		a.badRequest(c, "invalid report")
		return
	}
	run, err := a.store.CompleteDesktopTestRun(c.Request().Context(), c.Params().Get("id"), report)
	if err != nil {
		a.testLabError(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "test_run.report", "test_run", run.ID, &run.Spec.ProjectID, run.Status, nil)
	_ = c.JSON(envelope{Data: run})
}
