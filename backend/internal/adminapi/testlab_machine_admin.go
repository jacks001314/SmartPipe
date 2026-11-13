package adminapi

import (
	"github.com/kataras/iris/v12"
)

func (a *API) setTestMachineEnabled(c iris.Context) {
	c.SetMaxRequestBodySize(4096)
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if c.ReadJSON(&in) != nil || in.Enabled == nil {
		a.badRequest(c, "enabled required")
		return
	}
	id := c.Params().Get("id")
	if err := a.store.SetDesktopMachineEnabled(c.Request().Context(), id, *in.Enabled); err != nil {
		a.testLabError(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "test_machine.enabled", "test_machine", id, nil, "ok", map[string]any{"enabled": *in.Enabled})
	c.StatusCode(204)
}
func (a *API) recoverTestMachine(c iris.Context) {
	c.SetMaxRequestBodySize(4096)
	var in struct {
		Generation       int64  `json:"generation"`
		CleanupConfirmed bool   `json:"cleanup_confirmed"`
		Reason           string `json:"reason"`
	}
	if c.ReadJSON(&in) != nil || in.Generation < 0 || !in.CleanupConfirmed || len(in.Reason) < 3 {
		a.badRequest(c, "generation, cleanup confirmation and reason required")
		return
	}
	id := c.Params().Get("id")
	if err := a.store.RecoverDesktopMachine(c.Request().Context(), id, in.Generation); err != nil {
		a.testLabError(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "test_machine.recovered", "test_machine", id, nil, "ok", map[string]any{"generation": in.Generation, "reason": in.Reason})
	c.StatusCode(204)
}
