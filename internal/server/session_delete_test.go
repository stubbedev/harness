package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/app"
	"github.com/stubbedev/harness/internal/backend"
	"github.com/stubbedev/harness/internal/db"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/proto"
	"github.com/stubbedev/harness/internal/session"
)

// buildStoredWorkspace returns a controller over a workspace whose
// sessions live in a real database and whose coordinator reports the
// sessions in busy as running.
func buildStoredWorkspace(t *testing.T, busy map[string]bool) (*controllerV1, string, session.Service) {
	t.Helper()
	dataDir := t.TempDir()
	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Release(dataDir) })
	sessions := session.NewService(db.New(conn), conn)

	b := backend.New(context.Background(), nil, nil)
	wsID := uuid.New().String()
	a := &app.App{AgentCoordinator: &stubCoordinator{busy: busy}, Sessions: sessions}
	backend.InsertWorkspaceForTest(b, &backend.Workspace{ID: wsID, Path: t.TempDir(), App: a})
	return &controllerV1{backend: b, server: &Server{backend: b}}, wsID, sessions
}

func sessionRequest(t *testing.T, method, wsID, sid string, body []byte) *http.Request {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, "/v1/workspaces/"+wsID+"/sessions/"+sid, bytes.NewReader(body))
	req.SetPathValue("id", wsID)
	req.SetPathValue("sid", sid)
	return req
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) proto.Error {
	t.Helper()
	var got proto.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	return got
}

// TestDeleteSessionRefusedWhileASubagentRuns pins that a session whose
// child agent is still running is not deleted out from under it, and that
// the refusal is the same 409 session_busy a busy rewind gets.
func TestDeleteSessionRefusedWhileASubagentRuns(t *testing.T) {
	t.Parallel()
	busy := map[string]bool{}
	c, wsID, sessions := buildStoredWorkspace(t, busy)
	parent, err := sessions.Create(t.Context(), "parent")
	require.NoError(t, err)
	child, err := sessions.CreateTaskSession(t.Context(), "tool-call-1", parent.ID, "child")
	require.NoError(t, err)
	grandchild, err := sessions.CreateTaskSession(t.Context(), "tool-call-2", child.ID, "grandchild")
	require.NoError(t, err)
	busy[grandchild.ID] = true

	rec := httptest.NewRecorder()
	c.handleDeleteWorkspaceSession(rec, sessionRequest(t, http.MethodDelete, wsID, parent.ID, nil))
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, proto.ErrorCodeSessionBusy, decodeError(t, rec).Code)
	for _, id := range []string{parent.ID, child.ID, grandchild.ID} {
		_, err := sessions.Get(t.Context(), id)
		require.NoError(t, err, "a refused delete keeps %s", id)
	}

	delete(busy, grandchild.ID)
	rec = httptest.NewRecorder()
	c.handleDeleteWorkspaceSession(rec, sessionRequest(t, http.MethodDelete, wsID, parent.ID, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	for _, id := range []string{parent.ID, child.ID, grandchild.ID} {
		_, err := sessions.Get(t.Context(), id)
		require.Error(t, err, "an idle tree is deleted whole: %s", id)
	}
}

func TestDeleteMissingSessionIsNotFound(t *testing.T) {
	t.Parallel()
	c, wsID, _ := buildStoredWorkspace(t, nil)
	rec := httptest.NewRecorder()
	c.handleDeleteWorkspaceSession(rec, sessionRequest(t, http.MethodDelete, wsID, uuid.New().String(), nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, proto.ErrorCodeNotFound, decodeError(t, rec).Code)
}

// TestInvalidGoalIsABadRequest pins that a goal the validation refuses is
// the caller's mistake, not a server failure.
func TestInvalidGoalIsABadRequest(t *testing.T) {
	t.Parallel()
	c, wsID, sessions := buildStoredWorkspace(t, nil)
	s, err := sessions.Create(t.Context(), "goal")
	require.NoError(t, err)

	body, err := json.Marshal(proto.GoalRequest{Condition: strings.Repeat("x", session.MaxGoalConditionLength+1)})
	require.NoError(t, err)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/workspaces/"+wsID+"/agent/sessions/"+s.ID+"/goal", bytes.NewReader(body))
	req.SetPathValue("id", wsID)
	req.SetPathValue("sid", s.ID)
	rec := httptest.NewRecorder()
	c.handlePostWorkspaceAgentSessionGoal(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Equal(t, proto.ErrorCodeInvalidArgument, decodeError(t, rec).Code)
}

// TestAgentMessageCarriesAttachmentsOverTheGeneralBodyLimit pins that a
// prompt with an attachment the TUI accepts is decoded, even though its
// base64 is larger than the 1 MiB every other route allows. The
// workspace has no agent, so a decoded prompt is answered with the
// not-initialized error rather than a decode failure.
func TestAgentMessageCarriesAttachmentsOverTheGeneralBodyLimit(t *testing.T) {
	t.Parallel()
	b := backend.New(context.Background(), nil, nil)
	wsID := uuid.New().String()
	backend.InsertWorkspaceForTest(b, &backend.Workspace{ID: wsID, Path: t.TempDir(), App: &app.App{}})
	c := &controllerV1{backend: b, server: &Server{backend: b}}

	msg := proto.AgentMessage{
		SessionID: uuid.New().String(),
		Prompt:    "look",
		Attachments: []proto.Attachment{{
			FileName: "big.png",
			MimeType: "image/png",
			Content:  make([]byte, message.MaxAttachmentSize),
		}},
	}
	body, err := json.Marshal(msg)
	require.NoError(t, err)
	require.Greater(t, len(body), maxRequestBody)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/workspaces/"+wsID+"/agent", bytes.NewReader(body))
	req.SetPathValue("id", wsID)
	rec := httptest.NewRecorder()
	c.handlePostWorkspaceAgent(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, backend.ErrAgentNotInitialized.Error(), decodeError(t, rec).Message)
}

func TestAgentMessageBodyLimitFitsTheLargestPrompt(t *testing.T) {
	t.Parallel()
	attachments := int64(message.MaxAttachments * base64.StdEncoding.EncodedLen(message.MaxAttachmentSize))
	require.Equal(t, maxRequestBody+attachments, maxAgentMessageBody)
}

// TestOversizedBodyIsRefusedAsTooLarge pins that a body over the limit is
// reported as too large instead of as malformed JSON.
func TestOversizedBodyIsRefusedAsTooLarge(t *testing.T) {
	t.Parallel()
	b := backend.New(context.Background(), nil, nil)
	c := &controllerV1{backend: b, server: &Server{backend: b}}
	body := []byte(`{"title":"` + strings.Repeat("x", 64) + `"}`)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	_, ok := decodeBody[proto.SessionRenameRequest](c, rec, req, false, 16)
	require.False(t, ok)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	require.Equal(t, proto.ErrorCodeInvalidArgument, decodeError(t, rec).Code)
}
