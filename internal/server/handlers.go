package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/stubbedev/harness/internal/backend"
	"github.com/stubbedev/harness/internal/proto"
	"github.com/stubbedev/harness/internal/session"
)

// maxRequestBody bounds how much of a request body a handler decodes.
const maxRequestBody = 1 << 20

// wsFunc is the body of a workspace-scoped handler: it receives the
// resolved {id} workspace and returns the value to encode. A nil result
// answers 200 with no body.
type wsFunc func(ctx context.Context, ws *backend.Workspace) (any, error)

// serve resolves the {id} workspace, runs fn, and writes its result or
// error. Every workspace-scoped handler goes through it, so an unknown
// workspace always answers the 404 clients recover on.
func (c *controllerV1) serve(w http.ResponseWriter, r *http.Request, fn wsFunc) {
	c.serveStatus(w, r, http.StatusOK, fn)
}

// serveStatus is serve with a success status other than 200.
func (c *controllerV1) serveStatus(w http.ResponseWriter, r *http.Request, status int, fn wsFunc) {
	ws, err := c.backend.GetWorkspace(r.PathValue("id"))
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	out, err := fn(r.Context(), ws)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	if out == nil {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(out)
}

// serveBody is serve for handlers that take a JSON request body.
func serveBody[Req any](c *controllerV1, w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, ws *backend.Workspace, req Req) (any, error)) {
	serveDecoded(c, w, r, false, fn)
}

// serveOptionalBody is serveBody for endpoints whose body may be empty;
// an empty body decodes as the zero request.
func serveOptionalBody[Req any](c *controllerV1, w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, ws *backend.Workspace, req Req) (any, error)) {
	serveDecoded(c, w, r, true, fn)
}

func serveDecoded[Req any](c *controllerV1, w http.ResponseWriter, r *http.Request, allowEmpty bool, fn func(ctx context.Context, ws *backend.Workspace, req Req) (any, error)) {
	req, ok := decodeBody[Req](c, w, r, allowEmpty)
	if !ok {
		return
	}
	c.serve(w, r, func(ctx context.Context, ws *backend.Workspace) (any, error) {
		return fn(ctx, ws, req)
	})
}

// decodeBody decodes the JSON request body, writing a 400 and reporting
// false when it is malformed.
func decodeBody[Req any](c *controllerV1, w http.ResponseWriter, r *http.Request, allowEmpty bool) (Req, bool) {
	var req Req
	if r.Body == nil {
		return req, allowEmpty
	}
	err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBody)).Decode(&req)
	if err == nil || (allowEmpty && errors.Is(err, io.EOF)) {
		return req, true
	}
	c.server.logDebug(r, "Failed to decode request", "error", err)
	jsonError(w, http.StatusBadRequest, "failed to decode request")
	return req, false
}

// done adapts an operation that returns only an error to a wsFunc
// result.
func done(err error) (any, error) {
	return nil, err
}

// sessionOut converts a session for the wire with its read-time
// presence signals filled in.
func sessionOut(ws *backend.Workspace, s session.Session) proto.Session {
	out := proto.SessionFromDomain(s)
	out.IsBusy = isSessionBusy(ws, s.ID)
	out.AttachedClients = attachedClients(ws, s.ID)
	return out
}

// sessionResult is sessionOut for an operation returning a session.
func sessionResult(ws *backend.Workspace, s session.Session, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return sessionOut(ws, s), nil
}
