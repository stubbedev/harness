package server

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/stubbedev/harness/internal/backend"
)

// eventWriteTimeout bounds every write to an event stream. A client that
// keeps its stream open without reading it would otherwise hold its
// handler in a write forever; past the deadline the stream is dropped,
// and the client reconnects and resyncs.
const eventWriteTimeout = 10 * time.Second

func writeSSEFrame(w io.Writer, frame []byte) error {
	if _, err := io.WriteString(w, "data: "); err != nil {
		return err
	}
	if _, err := w.Write(frame); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n\n")
	return err
}

// writeEvent writes one SSE frame to an event stream and flushes it,
// failing when the client does not take it within timeout.
func writeEvent(rc *http.ResponseController, w io.Writer, frame []byte, timeout time.Duration) error {
	if err := rc.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return fmt.Errorf("setting the event write deadline: %w", err)
	}
	if err := writeSSEFrame(w, frame); err != nil {
		return fmt.Errorf("writing an event frame: %w", err)
	}
	if err := rc.Flush(); err != nil {
		return fmt.Errorf("flushing an event frame: %w", err)
	}
	return nil
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
