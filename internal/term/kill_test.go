//go:build !windows

package term

import (
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSession_CloseKillsTheWholeGroup pins the teardown guarantee: Close
// kills the shell's process group, so a background child that stayed in
// the group - and would otherwise survive holding the slave end open,
// keeping the session looking alive - dies with the session.
func TestSession_CloseKillsTheWholeGroup(t *testing.T) {
	s := startTestSession(t)
	waitReady(t, s)
	s.Drain()

	// A background child in the shell's process group.
	require.NoError(t, s.Send([]byte("sleep 9871 &\n")))
	require.True(t, s.WaitForQuiet(t.Context(), 2*time.Second, testTimeout(10*time.Second)),
		"the background job must start before the session is closed")

	pgid := s.proc.Pid
	s.Close()

	require.Eventually(t, func() bool {
		return syscall.Kill(-pgid, 0) != nil
	}, testTimeout(5*time.Second), 50*time.Millisecond,
		"the whole process group must be gone after Close")
}

// TestSession_CloseKillsSigintImmuneChildren: a child that ignores
// SIGINT still dies, because teardown SIGKILLs the group.
func TestSession_CloseKillsSigintImmuneChildren(t *testing.T) {
	s := startTestSession(t)
	waitReady(t, s)
	s.Drain()

	require.NoError(t, s.Send([]byte("{ trap '' INT; sleep 9872; } &\n")))
	require.True(t, s.WaitForQuiet(t.Context(), 2*time.Second, testTimeout(10*time.Second)))

	pgid := s.proc.Pid
	s.Close()

	require.Eventually(t, func() bool {
		return syscall.Kill(-pgid, 0) != nil
	}, testTimeout(5*time.Second), 50*time.Millisecond,
		"a SIGINT-immune child must still be killed by teardown")
}

// TestSession_CloseUnblocksTheReadLoop pins that a Close issued while
// the session is mid-wait unwinds it instead of hanging the caller.
func TestSession_CloseUnblocksTheReadLoop(t *testing.T) {
	s := startTestSession(t)
	waitReady(t, s)

	done := make(chan struct{})
	go func() {
		s.WaitForOutput(t.Context(), time.Hour)
		close(done)
	}()
	s.Close()

	select {
	case <-done:
	case <-time.After(testTimeout(5 * time.Second)):
		t.Fatal("Close must unblock a pending wait")
	}
}
