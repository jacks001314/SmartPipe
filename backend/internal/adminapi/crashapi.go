package adminapi

import (
	"strconv"

	"github.com/kataras/iris/v12"
)

func (a *API) registerCrashes(p iris.Party) {
	p.Get("/crashes", a.listCrashes)
	p.Get("/crashes/{id}", a.getCrash)
}

func (a *API) listCrashes(c iris.Context) {
	items, err := a.store.ListCrashReports(c.Request().Context(), intQuery(c, "limit", 100))
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) getCrash(c iris.Context) {
	id, err := strconv.ParseInt(c.Params().Get("id"), 10, 64)
	if err != nil || id <= 0 {
		a.badRequest(c, "非法 id")
		return
	}
	item, err := a.store.GetCrashReport(c.Request().Context(), id)
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: item})
}
