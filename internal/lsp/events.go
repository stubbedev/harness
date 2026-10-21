package lsp

import "time"

type EventType string

const (
	EventStateChanged       EventType = "state_changed"
	EventDiagnosticsChanged EventType = "diagnostics_changed"
)

type Event struct {
	Type            EventType
	Name            string
	State           ServerState
	Error           error
	DiagnosticCount int
}

type ClientInfo struct {
	Name            string
	State           ServerState
	Error           error
	DiagnosticCount int
	ConnectedAt     time.Time
	SessionDisabled bool
}

func (i ClientInfo) StatusText() string {
	if i.SessionDisabled {
		return "disabled for this session"
	}
	switch i.State {
	case StateUnstarted:
		return "not started yet"
	case StateStopped:
		return "stopped"
	case StateStarting:
		return "starting…"
	case StateReady:
		return "ready"
	case StateError:
		if i.Error != nil {
			return "error: " + i.Error.Error()
		}
		return "error"
	case StateDisabled:
		return "disabled"
	default:
		return "unknown"
	}
}
