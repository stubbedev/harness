//go:build linux

package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A foreground that reads and answers nothing - cat pointed at
// /dev/null, the shape of the wedge seen in the wild - keeps returning
// the waiting verdict, and the run of identical verdicts is counted:
// the first calls say no more than the state, and the escalations that
// follow name the wedge and the way out instead of confirming the
// caller's wrong mental model a fourth time.
//
// This is the kernel-backed contract: on Linux the sample reads the
// foreground's wait points, so a reader parked in read(2) is a waiting
// verdict, deterministically. On the ps platforms there are no wait
// points and the same state is deliberately heuristic (see
// waitingForInput): a sleeping echo-on reader is a command that is
// merely idle there, and a ps sample can miss under runner load - so
// pinning this here would only pin a race. The streak machine itself
// is covered everywhere by TestWaitingStreakStateMachine, and the
// escalation wording and boundary by TestWaitingHeader and
// TestWaitEscalated.
func TestPtyRunner_WaitingStreakEscalates(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Type(t.Context(), "cat > /dev/null", 10)
	require.NoError(t, err)
	require.True(t, res.Waiting)
	require.Equal(t, 1, res.WaitStreak)
	require.Zero(t, res.InputPending)

	for streak := 2; streak <= ptyWaitingEscalateCalls; streak++ {
		res, err = r.Type(t.Context(), "echo probe", 10)
		require.NoError(t, err)
		require.True(t, res.Waiting, "the wedge keeps the verdict")
		require.Equal(t, streak, res.WaitStreak)
		require.Zero(t, res.InputPending, "the wedge consumes what it is sent")
		require.Empty(t, res.Output)
	}
}

// A reset ends a waiting streak with the session it belongs to: the
// fresh shell starts counting from one.
func TestPtyRunner_ResetClearsWaitingStreak(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Type(t.Context(), "cat > /dev/null", 10)
	require.NoError(t, err)
	require.True(t, res.Waiting)
	res, err = r.Type(t.Context(), "echo probe", 10)
	require.NoError(t, err)
	require.Equal(t, 2, res.WaitStreak)

	require.NoError(t, r.Reset(t.Context()))
	require.Zero(t, r.waitStreak)

	res, err = r.Type(t.Context(), "cat > /dev/null", 10)
	require.NoError(t, err)
	require.True(t, res.Waiting)
	require.Equal(t, 1, res.WaitStreak, "the fresh shell starts a fresh streak")
}
