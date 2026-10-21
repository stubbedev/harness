package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/backend"
	"github.com/stubbedev/harness/internal/proto"
)

type wireRoute struct {
	method string
	path   string
	body   string
}

var sessionRoutes = []wireRoute{
	{http.MethodGet, "/sessions/{sid}", ""},
	{http.MethodPut, "/sessions/{sid}", `{"title":"t"}`},
	{http.MethodDelete, "/sessions/{sid}", ""},
	{http.MethodGet, "/sessions/{sid}/history", ""},
	{http.MethodGet, "/sessions/{sid}/messages", ""},
	{http.MethodGet, "/sessions/{sid}/messages/user", ""},
	{http.MethodGet, "/sessions/{sid}/checkpoints", ""},
	{http.MethodPost, "/sessions/{sid}/rewind", `{"message_id":"m","mode":"both"}`},
	{http.MethodGet, "/sessions/{sid}/prompt-history", ""},
	{http.MethodGet, "/sessions/{sid}/filetracker/files", ""},
	{http.MethodGet, "/agent/sessions/{sid}", ""},
	{http.MethodPost, "/agent/sessions/{sid}/cancel", ""},
	{http.MethodGet, "/agent/sessions/{sid}/prompts/queued", ""},
	{http.MethodGet, "/agent/sessions/{sid}/prompts/list", ""},
	{http.MethodPost, "/agent/sessions/{sid}/prompts/clear", ""},
	{http.MethodPost, "/agent/sessions/{sid}/summarize", `{}`},
	{http.MethodPost, "/agent/sessions/{sid}/goal", `{"condition":"c"}`},
	{http.MethodPost, "/agent/sessions/{sid}/shell", `{"command":"true"}`},
}

var malformedIDs = []string{
	"not-a-uuid",
	"..%2F..%2Fetc",
	strings.ToUpper(uuid.New().String()),
	"{" + uuid.New().String() + "}",
	"urn:uuid:" + uuid.New().String(),
	strings.ReplaceAll(uuid.New().String(), "-", ""),
}

func newRoutedServer(t *testing.T) (http.Handler, *backend.Workspace) {
	t.Helper()
	srv := &Server{}
	srv.backend = backend.New(context.Background(), nil, nil)
	srv.installHandler()
	ws := &backend.Workspace{ID: uuid.New().String(), Path: t.TempDir()}
	backend.InsertWorkspaceForTest(srv.backend, ws)
	return srv.Handler(), ws
}

func serveRoute(t *testing.T, h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sessionTarget(wsID string, route wireRoute, sid string) string {
	return "/v1/workspaces/" + wsID + strings.Replace(route.path, "{sid}", sid, 1)
}

func TestSessionRoutesRejectMalformedSessionID(t *testing.T) {
	t.Parallel()
	h, ws := newRoutedServer(t)
	for _, route := range sessionRoutes {
		for _, sid := range malformedIDs {
			rec := serveRoute(t, h, route.method, sessionTarget(ws.ID, route, sid), route.body)
			require.Equal(t, http.StatusBadRequest, rec.Code, "%s %s with sid %q", route.method, route.path, sid)
			require.Contains(t, rec.Body.String(), string(proto.ErrorCodeInvalidArgument))
			require.Contains(t, rec.Body.String(), "session_id")
		}
	}
}

func TestSessionRoutesAcceptCanonicalSessionID(t *testing.T) {
	t.Parallel()
	h, _ := newRoutedServer(t)
	missingWorkspace := uuid.New().String()
	for _, route := range sessionRoutes {
		rec := serveRoute(t, h, route.method, sessionTarget(missingWorkspace, route, uuid.New().String()), route.body)
		require.Equal(t, http.StatusNotFound, rec.Code, "%s %s", route.method, route.path)
	}
}

func TestWorkspacePromptHistoryWithoutSessionSkipsSessionParse(t *testing.T) {
	t.Parallel()
	h, _ := newRoutedServer(t)
	rec := serveRoute(t, h, http.MethodGet, "/v1/workspaces/"+uuid.New().String()+"/prompt-history", "")
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func clientRoutes(wsID string) []wireRoute {
	return []wireRoute{
		{http.MethodGet, "/v1/workspaces/" + wsID + "/events?client_id={cid}", ""},
		{http.MethodPost, "/v1/workspaces/" + wsID + "/current-session?client_id={cid}", `{"session_id":""}`},
		{http.MethodDelete, "/v1/workspaces/" + wsID + "?client_id={cid}", ""},
		{http.MethodDelete, "/v1/clients/{cid}", ""},
	}
}

func TestClientRoutesRejectMalformedClientID(t *testing.T) {
	t.Parallel()
	h, ws := newRoutedServer(t)
	for _, route := range clientRoutes(ws.ID) {
		for _, cid := range append([]string{""}, malformedIDs...) {
			target := strings.Replace(route.path, "{cid}", cid, 1)
			if cid == "" && strings.HasPrefix(route.path, "/v1/clients/") {
				continue
			}
			rec := serveRoute(t, h, route.method, target, route.body)
			require.Equal(t, http.StatusBadRequest, rec.Code, "%s %s with client_id %q", route.method, route.path, cid)
			require.Contains(t, rec.Body.String(), "client_id")
		}
	}
}

func TestClientRoutesAcceptCanonicalClientID(t *testing.T) {
	t.Parallel()
	h, ws := newRoutedServer(t)
	cid := uuid.New().String()
	rec := serveRoute(t, h, http.MethodDelete, "/v1/clients/"+cid, "")
	require.Equal(t, http.StatusOK, rec.Code)
	rec = serveRoute(t, h, http.MethodDelete, "/v1/workspaces/"+ws.ID+"?client_id="+cid, "")
	require.Equal(t, http.StatusOK, rec.Code)
	rec = serveRoute(t, h, http.MethodPost, "/v1/workspaces/"+ws.ID+"/current-session?client_id="+cid, `{"session_id":""}`)
	require.Equal(t, http.StatusConflict, rec.Code)
}

func TestRewindRejectsUnknownMode(t *testing.T) {
	t.Parallel()
	h, ws := newRoutedServer(t)
	target := "/v1/workspaces/" + ws.ID + "/sessions/" + uuid.New().String() + "/rewind"
	for _, mode := range []string{"bogus", "BOTH", "file", " both"} {
		rec := serveRoute(t, h, http.MethodPost, target, `{"message_id":"m","mode":"`+mode+`"}`)
		require.Equal(t, http.StatusBadRequest, rec.Code, "mode %q", mode)
	}
}
