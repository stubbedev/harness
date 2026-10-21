package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"

	"github.com/stubbedev/harness/internal/backend"
	"github.com/stubbedev/harness/internal/proto"
	"github.com/stubbedev/harness/internal/pubsub"
)

func wrapEvent(ev any) *pubsub.Payload {
	p, err := proto.WrapEvent(ev)
	switch {
	case err == nil:
		return p
	case errors.Is(err, proto.ErrNoWireForm):
		slog.Debug("Dropping event with no wire form for SSE", "error", err)
	default:
		slog.Warn("Failed to wrap event for SSE", "error", err)
	}
	return nil
}

// writeSSEPayload writes p as one SSE data frame. The inner payload is
// already encoded JSON; framing the envelope by hand spares re-encoding
// it, which for a streaming message is the whole transcript entry again
// on every update.
func writeSSEPayload(w io.Writer, p *pubsub.Payload) {
	typ, _ := json.Marshal(p.Type)
	_, _ = io.WriteString(w, `data: {"type":`)
	_, _ = w.Write(typ)
	_, _ = io.WriteString(w, `,"payload":`)
	_, _ = w.Write(p.Payload)
	_, _ = io.WriteString(w, "}\n\n")
}

// isSessionBusy reports whether the given workspace has an in-flight
// agent run for sessionID. It tolerates a nil workspace (treating it as
// "not busy") so REST handlers can pass GetWorkspace's result through
// unconditionally — the workspace lookup error is already surfaced by
// the prior ListSessions/GetSession call when relevant.
func isSessionBusy(ws *backend.Workspace, sessionID string) bool {
	if ws == nil || ws.App == nil || ws.AgentCoordinator == nil {
		return false
	}
	return ws.AgentCoordinator.IsSessionBusy(sessionID)
}

// attachedClients returns the number of clients currently viewing
// sessionID in ws. Hold-only clients (streams == 0) do not contribute.
// A nil workspace is treated as zero so handlers can pass GetWorkspace's
// result through without an extra guard.
func attachedClients(ws *backend.Workspace, sessionID string) int {
	if ws == nil {
		return 0
	}
	return ws.AttachedClientsForSession(sessionID)
}
