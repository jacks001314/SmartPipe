package adminapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/store"
	"smartpipe/backend/internal/testlab"
)

// The scheduler owns source acquisition. It stages every engine input as an
// immutable blob on the session; the runner only downloads, verifies and uses
// it. This endpoint is the single write path for those blobs.
func (a *API) putTestSessionInput(c iris.Context) {
	c.SetMaxRequestBodySize(store.MaxTestSessionInputBytes + 1)
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, store.MaxTestSessionInputBytes+1))
	if err != nil || len(body) == 0 || len(body) > store.MaxTestSessionInputBytes {
		a.badRequest(c, "input must be a non-empty blob under the size limit")
		return
	}
	sum := sha256.Sum256(body)
	mime := strings.TrimSpace(c.GetHeader("Content-Type"))
	if mime == "" {
		mime = "application/gzip"
	}
	in := store.DesktopSessionInput{
		Name: c.Params().Get("name"), Kind: strings.TrimSpace(c.URLParam("kind")),
		SHA256: hex.EncodeToString(sum[:]), MIMEType: mime, Bytes: int64(len(body)), Payload: body,
	}
	declared := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(c.URLParam("sha256"))), "sha256:")
	if declared != "" && declared != in.SHA256 {
		a.badRequest(c, "uploaded bytes do not match the requested sha256")
		return
	}
	if err := a.store.PutTestSessionInput(c.Request().Context(), c.Params().Get("id"), in); err != nil {
		a.testLabError(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "test_session.input", "test_session", c.Params().Get("id"), nil, "ok",
		map[string]any{"name": in.Name, "sha256": in.SHA256, "bytes": in.Bytes})
	c.StatusCode(http.StatusCreated)
	_ = c.JSON(envelope{Data: iris.Map{"name": in.Name, "sha256": in.SHA256, "bytes": in.Bytes}})
}

func (a *API) listTestSessionInputs(c iris.Context) {
	items, err := a.store.ListTestSessionInputs(c.Request().Context(), c.Params().Get("id"))
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) machineListTestSessionInputs(c iris.Context) {
	generation, _ := strconv.ParseInt(c.URLParam("generation"), 10, 64)
	items, err := a.store.ListTestSessionMachineInputs(c.Request().Context(), c.Values().GetString("test_machine"),
		c.Params().Get("id"), generation)
	if err != nil {
		a.testLabError(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) machineGetTestSessionInput(c iris.Context) {
	generation, _ := strconv.ParseInt(c.URLParam("generation"), 10, 64)
	item, err := a.store.GetTestSessionMachineInput(c.Request().Context(), c.Values().GetString("test_machine"),
		c.Params().Get("id"), generation, c.Params().Get("name"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.StatusCode(http.StatusNotFound)
			return
		}
		a.testLabError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Type", item.MIMEType)
	c.Header("X-Content-SHA256", item.SHA256)
	_, _ = c.Write(item.Payload)
}

func (a *API) uploadTestSessionCheck(c iris.Context) {
	c.SetMaxRequestBodySize(64 << 10)
	var in struct {
		SessionID  string `json:"session_id"`
		Generation int64  `json:"generation"`
		Check      struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			Evidence string `json:"evidence"`
			Note     string `json:"note"`
		} `json:"check"`
	}
	if c.ReadJSON(&in) != nil || in.SessionID == "" || in.Generation < 1 {
		a.badRequest(c, "invalid check")
		return
	}
	err := a.store.UpsertTestSessionMachineCheck(c.Request().Context(), c.Values().GetString("test_machine"),
		in.SessionID, in.Generation, testlab.SessionCheck{ID: in.Check.ID, Status: in.Check.Status,
			Evidence: in.Check.Evidence, Note: in.Check.Note})
	if err != nil {
		a.testLabError(c, err)
		return
	}
	c.StatusCode(http.StatusNoContent)
}

func (a *API) uploadTestSessionFinding(c iris.Context) {
	c.SetMaxRequestBodySize(64 << 10)
	var in struct {
		SessionID  string          `json:"session_id"`
		Generation int64           `json:"generation"`
		Finding    testlab.Finding `json:"finding"`
	}
	if c.ReadJSON(&in) != nil || in.SessionID == "" || in.Generation < 1 {
		a.badRequest(c, "invalid finding")
		return
	}
	if err := a.store.UpsertTestSessionMachineFinding(c.Request().Context(), c.Values().GetString("test_machine"),
		in.SessionID, in.Generation, in.Finding); err != nil {
		a.testLabError(c, err)
		return
	}
	c.StatusCode(http.StatusNoContent)
}
