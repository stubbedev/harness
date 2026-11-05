package server

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent"
	"github.com/stubbedev/harness/internal/backend"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/proto"
	"github.com/stubbedev/harness/internal/session"
)

// TestClassifyError pins the status and code for the errors clients act
// on. Only a lost workspace may carry workspace_not_found: clients
// re-register on it, so a missing session or LSP server must not.
func TestClassifyError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err    error
		status int
		code   proto.ErrorCode
	}{
		{backend.ErrWorkspaceNotFound, http.StatusNotFound, proto.ErrorCodeWorkspaceNotFound},
		{fmt.Errorf("get: %w", sql.ErrNoRows), http.StatusNotFound, proto.ErrorCodeNotFound},
		{backend.ErrLSPClientNotFound, http.StatusNotFound, proto.ErrorCodeNotFound},
		{fmt.Errorf("cannot rewind: %w", backend.ErrSessionBusy), http.StatusConflict, proto.ErrorCodeSessionBusy},
		{fmt.Errorf("compact: %w", agent.ErrSessionBusy), http.StatusConflict, proto.ErrorCodeSessionBusy},
		{fmt.Errorf("%w: mode", backend.ErrInvalidArgument), http.StatusBadRequest, proto.ErrorCodeInvalidArgument},
		{fmt.Errorf("set goal: %w", session.ErrInvalidGoal), http.StatusBadRequest, proto.ErrorCodeInvalidArgument},
		{fmt.Errorf("send: %w", message.ErrAttachmentsRefused), http.StatusBadRequest, proto.ErrorCodeInvalidArgument},
		{fmt.Errorf("cannot delete: %w", backend.ErrSessionBusy), http.StatusConflict, proto.ErrorCodeSessionBusy},
		{backend.ErrServerShuttingDown, http.StatusServiceUnavailable, proto.ErrorCodeUnavailable},
		{errors.New("boom"), http.StatusInternalServerError, proto.ErrorCodeInternal},
	}
	for _, tc := range tests {
		got := classifyError(tc.err)
		require.Equal(t, tc.status, got.status, tc.err.Error())
		require.Equal(t, tc.code, got.code, tc.err.Error())
	}
}
