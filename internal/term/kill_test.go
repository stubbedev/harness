//go:build !windows

package term

import (
	"os/exec"
	"runtime"
	"strings"
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

// TestSession_CloseReapsSetsidEscapees: a child that escaped the group
// with setsid still dies with the session - caught by the PPid walk
// while the shell parents it, and by the sweep of processes still
// holding the slave device.
func TestSession_CloseReapsSetsidEscapees(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the escapee sweep is linux-only")
	}
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("no setsid on PATH")
	}
	s := startTestSession(t)
	waitReady(t, s)
	s.Drain()

	require.NoError(t, s.Send([]byte("setsid sleep 9873 &\n")))
	require.True(t, s.WaitForQuiet(t.Context(), 2*time.Second, testTimeout(10*time.Second)),
		"the escaped child must start before the session is closed")

	s.Close()

	require.Eventually(t, func() bool {
		out, err := exec.CommandContext(t.Context(), "pgrep", "-f", "sleep 9873").Output()
		return err != nil || len(strings.TrimSpace(string(out))) == 0
	}, testTimeout(5*time.Second), 50*time.Millisecond,
		"a setsid escapee must not survive the session")
}
