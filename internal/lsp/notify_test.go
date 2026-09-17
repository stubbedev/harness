package lsp

import (
	"context"
	"testing"
	"time"
)

func TestAwaitSettledReturnsImmediatelyWithNothingPending(t *testing.T) {
	t.Parallel()
	s := &Manager{}

	start := time.Now()
	s.AwaitSettled(t.Context(), time.Second)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("waited %s with nothing in flight", elapsed)
	}
}

func TestAwaitSettledWaitsForABackgroundSettle(t *testing.T) {
	t.Parallel()
	s := &Manager{}
	// A nil client settles as soon as it is waited on, which is enough to
	// exercise registration and cleanup.
	s.settleInBackground(t.Context(), []*Client{nil})

	s.AwaitSettled(t.Context(), time.Second)

	s.settleMu.Lock()
	pending := len(s.settlePending)
	s.settleMu.Unlock()
	if pending != 0 {
		t.Fatalf("settle left %d pending entries behind", pending)
	}
}

func TestAwaitSettledGivesUpAtTheBudget(t *testing.T) {
	t.Parallel()
	s := &Manager{}
	stuck := make(chan struct{})
	s.settleMu.Lock()
	s.settlePending = append(s.settlePending, stuck)
	s.settleMu.Unlock()
	defer close(stuck)

	start := time.Now()
	s.AwaitSettled(t.Context(), 50*time.Millisecond)
	elapsed := time.Since(start)
	if elapsed < 50*time.Millisecond {
		t.Fatalf("returned after %s, before the budget", elapsed)
	}
	if elapsed > time.Second {
		t.Fatalf("waited %s past a 50ms budget", elapsed)
	}
}

func TestAwaitSettledStopsOnCanceledContext(t *testing.T) {
	t.Parallel()
	s := &Manager{}
	stuck := make(chan struct{})
	s.settleMu.Lock()
	s.settlePending = append(s.settlePending, stuck)
	s.settleMu.Unlock()
	defer close(stuck)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	start := time.Now()
	s.AwaitSettled(ctx, time.Minute)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waited %s on a canceled context", elapsed)
	}
}

func TestAwaitSettledZeroBudgetNeverWaits(t *testing.T) {
	t.Parallel()
	s := &Manager{}
	stuck := make(chan struct{})
	s.settleMu.Lock()
	s.settlePending = append(s.settlePending, stuck)
	s.settleMu.Unlock()
	defer close(stuck)

	start := time.Now()
	s.AwaitSettled(t.Context(), 0)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("zero budget waited %s with a settle in flight", elapsed)
	}
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
	s.AwaitSettled(t.Context(), time.Second)
	if s.Settling() {
		t.Error("nil manager reported itself settling")
	}
	if s.Ledger() != nil {
		t.Fatal("nil manager handed out a ledger")
	}
}
