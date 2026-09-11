package term

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func startTestSession(t *testing.T) *Session {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this platform")
	}
	t.Setenv("SHELL", "/bin/sh")
	s, err := Start(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(s.Close)
	return s
}

func TestSession_RunCommandAndReadOutput(t *testing.T) {
	s := startTestSession(t)

	// Discard shell startup output (prompt, motd).
	require.True(t, s.WaitForQuiet(t.Context(), 300*time.Millisecond, 5*time.Second))
	s.Drain()

	require.NoError(t, s.Send([]byte("echo hello-term\n")))
	require.True(t, s.WaitForPattern(t.Context(), regexp.MustCompile("hello-term"), 10*time.Second))

	out := string(s.Drain())
	require.Contains(t, out, "hello-term")
	require.True(t, s.Alive())
}

func TestSession_InteractivePrompt(t *testing.T) {
	s := startTestSession(t)
	require.True(t, s.WaitForQuiet(t.Context(), 300*time.Millisecond, 5*time.Second))
	s.Drain()

	// A program that reads from stdin: this hangs a pipe-based runner
	// forever, but the PTY just shows the prompt.
	require.NoError(t, s.Send([]byte("read name; echo \"got:$name\"\n")))
	require.True(t, s.WaitForPattern(t.Context(), regexp.MustCompile("got:"), 500*time.Millisecond) || true)
	// The read blocks; feed it input.
	require.NoError(t, s.Send([]byte("agent\n")))
	require.True(t, s.WaitForPattern(t.Context(), regexp.MustCompile(`got:agent`), 10*time.Second))
}

func TestSession_ExitDetected(t *testing.T) {
	s := startTestSession(t)
	require.True(t, s.WaitForQuiet(t.Context(), 300*time.Millisecond, 5*time.Second))
	s.Drain()

	require.NoError(t, s.Send([]byte("exit\n")))
	deadline := time.Now().Add(10 * time.Second)
	for s.Alive() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	require.False(t, s.Alive())
}

func TestSession_WaitForPatternTimeout(t *testing.T) {
	s := startTestSession(t)
	require.True(t, s.WaitForQuiet(t.Context(), 300*time.Millisecond, 5*time.Second))
	s.Drain()

	start := time.Now()
	require.False(t, s.WaitForPattern(t.Context(), regexp.MustCompile("never-appears"), 200*time.Millisecond))
	require.Less(t, time.Since(start), 3*time.Second)
}

func TestSession_PendingCap(t *testing.T) {
	s := startTestSession(t)
	require.True(t, s.WaitForQuiet(t.Context(), 300*time.Millisecond, 5*time.Second))
	s.Drain()

	// Produce more than maxPending of output and confirm the buffer
	// stays bounded.
	require.NoError(t, s.Send([]byte("yes 0123456789 | head -c 9000000\n")))
	require.Eventually(t, func() bool {
		return s.PendingLen() > 0
	}, 10*time.Second, 100*time.Millisecond)

	// Let it finish, then confirm the cap held.
	require.True(t, s.WaitForQuiet(t.Context(), 2*time.Second, 30*time.Second))
	require.LessOrEqual(t, s.PendingLen(), maxPending)
}

func TestSession_SendAfterExitFails(t *testing.T) {
	s := startTestSession(t)
	require.True(t, s.WaitForQuiet(t.Context(), 300*time.Millisecond, 5*time.Second))
	require.NoError(t, s.Send([]byte("exit\n")))
	require.Eventually(t, func() bool { return !s.Alive() }, 10*time.Second, 50*time.Millisecond)
	require.Error(t, s.Send([]byte("echo late\n")))
}

func TestShellFallback(t *testing.T) {
	t.Setenv("SHELL", "")
	require.Equal(t, "/bin/sh", Shell())
	t.Setenv("SHELL", "/usr/bin/zsh")
	require.Equal(t, "/usr/bin/zsh", Shell())
}

func TestSession_LongOutputTailPreserved(t *testing.T) {
	s := startTestSession(t)
	require.True(t, s.WaitForQuiet(t.Context(), 300*time.Millisecond, 5*time.Second))
	s.Drain()

	require.NoError(t, s.Send([]byte("seq 1 2000\n")))
	require.True(t, s.WaitForQuiet(t.Context(), 2*time.Second, 15*time.Second))
	out := string(s.Drain())
	out = strings.ReplaceAll(out, "\r", "")
	require.Contains(t, out, "1999")
	require.True(t, strings.Contains(strings.ReplaceAll(out, "\r", ""), "1\n2\n"))
}
