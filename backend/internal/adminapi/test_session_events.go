package adminapi

import (
	"net/http"
	"strconv"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/testlab"
)

func (a *API) listTestSessionEvents(c iris.Context) {
	after, _ := strconv.Atoi(c.URLParam("after_seq"))
	limit, _ := strconv.Atoi(c.URLParam("limit"))
	items, err := a.store.ListTestSessionEvents(c.Request().Context(), c.Params().Get("id"), after, limit)
	if err != nil {
		a.testLabError(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) uploadTestSessionEvents(c iris.Context) {
	c.SetMaxRequestBodySize(1 << 20)
	var in struct {
		SessionID  string                 `json:"session_id"`
		Generation int64                  `json:"generation"`
		Events     []testlab.SessionEvent `json:"events"`
	}
	if c.ReadJSON(&in) != nil || in.SessionID == "" || in.Generation < 1 || len(in.Events) == 0 {
		c.StopWithStatus(http.StatusBadRequest)
		return
	}
	if err := a.store.AppendTestSessionEvents(c.Request().Context(), c.Values().GetString("test_machine"),
		in.SessionID, in.Generation, in.Events); err != nil {
		a.testLabError(c, err)
		return
	}
	_ = c.JSON(envelope{Data: iris.Map{"accepted": len(in.Events)}})
}
