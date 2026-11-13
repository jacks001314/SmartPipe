package adminapi

import (
	"github.com/kataras/iris/v12"
	"smartpipe/backend/internal/store"
)

func (a *API) uploadTestEvidence(c iris.Context) {
	c.SetMaxRequestBodySize(12 << 20)
	var in struct {
		RunID      string                `json:"run_id"`
		Generation int64                 `json:"generation"`
		Evidence   store.DesktopEvidence `json:"evidence"`
	}
	if c.ReadJSON(&in) != nil || in.RunID == "" || in.Generation < 1 {
		a.badRequest(c, "invalid evidence upload")
		return
	}
	ref, err := a.store.PutDesktopEvidence(c.Request().Context(), c.Values().GetString("test_machine"), in.RunID, in.Generation, in.Evidence)
	if err != nil {
		a.testLabError(c, err)
		return
	}
	_ = c.JSON(envelope{Data: iris.Map{"reference": ref}})
}
func (a *API) getTestEvidence(c iris.Context) {
	e, err := a.store.GetDesktopEvidence(c.Request().Context(), c.Params().Get("id"), c.Params().Get("digest"))
	if err != nil {
		a.testLabError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Disposition", "attachment")
	c.ContentType(e.MIMEType)
	_, _ = c.Write(e.Payload)
}
