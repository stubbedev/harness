package lsp

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A background settle registers itself while it runs and removes itself when
// it is done, so Settling goes quiet again on its own; nothing has to wait on
// it for the entry to be cleaned up.
func TestSettleInBackgroundCleansUpAfterItself(t *testing.T) {
	t.Parallel()
	s := &Manager{}
	// A nil client settles as soon as it is waited on, which is enough to
	// exercise registration and cleanup.
	s.settleInBackground(t.Context(), []*Client{nil})

	require.Eventually(t, func() bool { return !s.Settling() }, 5*time.Second, 5*time.Millisecond)
	s.settleMu.Lock()
	pending := len(s.settlePending)
	s.settleMu.Unlock()
	require.Zero(t, pending, "settle left pending entries behind")
}

func TestSettlingTracksInFlightWaits(t *testing.T) {
	t.Parallel()
	s := &Manager{}
	if s.Settling() {
		t.Fatal("settling with nothing in flight")
	}

	stuck := make(chan struct{})
	s.settleMu.Lock()
	s.settlePending = append(s.settlePending, stuck)
	s.settleMu.Unlock()
	if !s.Settling() {
		t.Fatal("not settling with a wait in flight")
	}

	s.settleMu.Lock()
	s.settlePending = deleteChan(s.settlePending, stuck)
	s.settleMu.Unlock()
	close(stuck)
	if s.Settling() {
		t.Fatal("still settling after the wait finished")
	}
}

func TestSettleInBackgroundIgnoresAnEmptyClientList(t *testing.T) {
	t.Parallel()
	s := &Manager{}
	s.settleInBackground(t.Context(), nil)

	s.settleMu.Lock()
	pending := len(s.settlePending)
	s.settleMu.Unlock()
	if pending != 0 {
		t.Fatalf("registered %d waits for no clients", pending)
	}
}

func TestNotifyOnNilManagerIsInert(t *testing.T) {
	t.Parallel()
	var s *Manager
	s.NotifyChangeAsync(t.Context(), "/tmp/whatever.go")
	s.NotifyWorkspaceChangeAsync(t.Context())
	if s.Settling() {
		t.Error("nil manager reported itself settling")
	}
	if s.Ledger() != nil {
		t.Fatal("nil manager handed out a ledger")
	}
}
