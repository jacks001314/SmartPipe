package adminapi

import (
	"github.com/kataras/iris/v12"
	"smartpipe/backend/internal/store"
)

func (a *API) getDesktopGate(c iris.Context) {
	policies, err := a.store.GetDesktopGatePolicies(c.Request().Context(), c.Params().Get("id"))
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: policies})
}
func (a *API) setDesktopGate(c iris.Context) {
	c.SetMaxRequestBodySize(64 << 10)
	var policies []store.DesktopGatePolicy
	if c.ReadJSON(&policies) != nil {
		a.badRequest(c, "invalid gate policy")
		return
	}
	id := c.Params().Get("id")
	if err := a.store.SetDesktopGatePolicies(c.Request().Context(), id, policies); err != nil {
		a.testLabError(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "test_gate.set", "project", id, &id, "ok", map[string]any{"policies": policies})
	_ = c.JSON(envelope{Data: policies})
}
