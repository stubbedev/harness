package app

import (
	"context"
	"time"

	"github.com/stubbedev/harness/internal/csync"
	"github.com/stubbedev/harness/internal/lsp"
	"github.com/stubbedev/harness/internal/pubsub"
)

// LSPEventType represents the type of LSP event
type LSPEventType string

const (
	LSPEventStateChanged       LSPEventType = "state_changed"
	LSPEventDiagnosticsChanged LSPEventType = "diagnostics_changed"
)

// LSPEvent represents an event in the LSP system
type LSPEvent struct {
	Type            LSPEventType
	Name            string
	State           lsp.ServerState
	Error           error
	DiagnosticCount int
}

// LSPClientInfo holds information about an LSP client's state
type LSPClientInfo struct {
	Name            string
	State           lsp.ServerState
	Error           error
	Client          *lsp.Client
	DiagnosticCount int
	ConnectedAt     time.Time
	// SessionDisabled marks a server turned off at runtime for the rest
	// of the process, so the UI can tell it from a merely stopped one.
	// It is filled in by the state readers, not updateLSPState, because
	// the mark lives in the manager rather than the state map.
	SessionDisabled bool
}

var (
	lspStates = csync.NewMap[string, LSPClientInfo]()
	lspBroker = pubsub.NewBroker[LSPEvent]()
)

// SubscribeLSPEvents returns a channel for LSP events
func SubscribeLSPEvents(ctx context.Context) <-chan pubsub.Event[LSPEvent] {
	return lspBroker.Subscribe(ctx)
}

// GetLSPStates returns the current state of all LSP clients
func GetLSPStates() map[string]LSPClientInfo {
	return lspStates.Copy()
}

// GetLSPState returns the state of a specific LSP client
func GetLSPState(name string) (LSPClientInfo, bool) {
	return lspStates.Get(name)
}

// LSPStatesWithSessionDisabled returns the LSP states with the
// manager's runtime session-disable marks applied: marked servers are
// flagged so readers can tell them from merely stopped ones, and a
// marked server without a state entry gets a stopped entry of its own.
func LSPStatesWithSessionDisabled(m *lsp.Manager) map[string]LSPClientInfo {
	states := GetLSPStates()
	for name := range m.SessionDisabled() {
		info, ok := states[name]
		if !ok {
			info = LSPClientInfo{Name: name, State: lsp.StateStopped}
		}
		info.SessionDisabled = true
		states[name] = info
	}
	return states
}

// updateLSPState updates the state of an LSP client and publishes an event
func updateLSPState(name string, state lsp.ServerState, err error, client *lsp.Client, diagnosticCount int) {
	info := LSPClientInfo{
		Name:            name,
		State:           state,
		Error:           err,
		Client:          client,
		DiagnosticCount: diagnosticCount,
	}
	if state == lsp.StateReady {
		info.ConnectedAt = time.Now()
	} else if existing, ok := lspStates.Get(name); ok {
		info.ConnectedAt = existing.ConnectedAt
	}
	lspStates.Set(name, info)

	// Publish state change event
	lspBroker.Publish(pubsub.UpdatedEvent, LSPEvent{
		Type:            LSPEventStateChanged,
		Name:            name,
		State:           state,
		Error:           err,
		DiagnosticCount: diagnosticCount,
	})
}

// updateLSPDiagnostics updates the diagnostic count for an LSP client and publishes an event
func updateLSPDiagnostics(name string, diagnosticCount int) {
	if info, exists := lspStates.Get(name); exists {
		info.DiagnosticCount = diagnosticCount
		lspStates.Set(name, info)

		// Publish diagnostics change event
		lspBroker.Publish(pubsub.UpdatedEvent, LSPEvent{
			Type:            LSPEventDiagnosticsChanged,
			Name:            name,
			State:           info.State,
			Error:           info.Error,
			DiagnosticCount: diagnosticCount,
		})
	}
}
