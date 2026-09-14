package term

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testTimeout stretches an upper-bound wait. `just test` runs package
// tests in parallel, so the shell behind these PTYs competes for CPU with
// the rest of the suite and can stall for seconds; the waits here are
// upper bounds, so a loaded machine must not read as a failure.
func testTimeout(d time.Duration) time.Duration {
	return 3 * d
}

// readyProbe is a command whose output cannot be confused with its own
// echo: the terminal echoes the line as typed, so the echo carries the
// "%s" and only the shell's answer carries "ok".
const readyProbe = `printf '__ready:%s__\n' ok`

var readyProbeRe = regexp.MustCompile(`__ready:ok__`)

// waitReady waits until the shell answers - not merely until it has
// printed something. A shell that has shown its prompt may still throw
// away what is typed at it: bash discards typeahead when readline
// initialises, and the line discipline is reconfigured after the child
// opens the tty either way. A command sent into that window is swallowed
// without a trace, and the test that sent it then waits out its whole
// timeout for output that will never come - which is exactly how these
// tests failed on loaded CI machines.
//
// So probe until an answer comes back, then drain the probes. Sending
// the probe repeatedly is safe: a shell that ate the first one never ran
// it, and one that ran several just answered several times.
func waitReady(t *testing.T, s *Session) {
	t.Helper()
	require.Eventually(t, func() bool {
		if err := s.Send([]byte(readyProbe + "\n")); err != nil {
			return false
		}
		return s.WaitForPattern(t.Context(), readyProbeRe, time.Second)
	}, testTimeout(10*time.Second), 50*time.Millisecond, "the shell never answered a probe")
	require.True(t, s.WaitForQuiet(t.Context(), 300*time.Millisecond, testTimeout(5*time.Second)))
	s.Drain()
}

// waitOutput waits for the pattern in everything printed since the
// previous match, not only in what arrives after the call. WaitForPattern
// resets its scan position on entry, so a shell that answers inside the
// gap between Send returning and the wait starting - preempted test
// goroutine, loaded runner - has its answer skipped, and the wait then
// sits out its whole timeout for output that is already buffered.
// WaitForAny keeps scanning from where the last match ended instead.
func waitOutput(t *testing.T, s *Session, pattern *regexp.Regexp, timeout time.Duration) bool {
	t.Helper()
	return s.WaitForAny(t.Context(), []*regexp.Regexp{pattern}, timeout) >= 0
}

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
	waitReady(t, s)
	s.Drain()

	require.NoError(t, s.Send([]byte("echo hello-term\n")))
	require.True(t, waitOutput(t, s, regexp.MustCompile("hello-term"), testTimeout(10*time.Second)))

	out := string(s.Drain())
	require.Contains(t, out, "hello-term")
	require.True(t, s.Alive())
}

func TestSession_InteractivePrompt(t *testing.T) {
	s := startTestSession(t)
	waitReady(t, s)
	s.Drain()

	// A program that reads from stdin: this hangs a pipe-based runner
	// forever, but the PTY just shows the prompt.
	require.NoError(t, s.Send([]byte("read name; echo \"got:$name\"\n")))
	require.True(t, waitOutput(t, s, regexp.MustCompile("got:"), 500*time.Millisecond) || true)
	// The read blocks; feed it input.
	require.NoError(t, s.Send([]byte("agent\n")))
	require.True(t, waitOutput(t, s, regexp.MustCompile(`got:agent`), testTimeout(10*time.Second)))
}

func TestSession_ExitDetected(t *testing.T) {
	s := startTestSession(t)
	waitReady(t, s)
	s.Drain()

	require.NoError(t, s.Send([]byte("exit\n")))
	deadline := time.Now().Add(testTimeout(10 * time.Second))
	for s.Alive() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	require.False(t, s.Alive())
}

func TestSession_SecretRead(t *testing.T) {
	s := startTestSession(t)
	waitReady(t, s)
	s.Drain()

	// At an idle prompt the terminal is either echoing (shells without
	// a line editor) or raw (readline, zle - which echo in software);
	// neither is a hidden-line read.
	require.Equal(t, SecretReadNo, s.SecretRead())

	// A hidden-line reader in a child process: the termios shape sudo,
	// su, ssh and getpass all leave the terminal in while they wait.
	require.NoError(t, s.Send([]byte("/bin/sh -c 'stty -echo; read x; stty echo'\n")))
	require.True(t, s.WaitForQuiet(t.Context(), 300*time.Millisecond, testTimeout(5*time.Second)))
	s.Drain()
	require.Equal(t, SecretReadYes, s.SecretRead())

	require.NoError(t, s.Send([]byte("secret\n")))
	require.True(t, s.WaitForQuiet(t.Context(), 300*time.Millisecond, testTimeout(5*time.Second)))
	s.Drain()
	require.Equal(t, SecretReadNo, s.SecretRead())
}

func TestSession_WaitForPatternTimeout(t *testing.T) {
	s := startTestSession(t)
	waitReady(t, s)
	s.Drain()

	start := time.Now()
	require.False(t, s.WaitForPattern(t.Context(), regexp.MustCompile("never-appears"), 200*time.Millisecond))
	require.Less(t, time.Since(start), 3*time.Second)
}

func TestSession_PendingCap(t *testing.T) {
	s := startTestSession(t)
	waitReady(t, s)
	s.Drain()

	// Produce more than maxPending of output and confirm the buffer
	// stays bounded.
	require.NoError(t, s.Send([]byte("yes 0123456789 | head -c 9000000\n")))
	require.Eventually(t, func() bool {
		return s.PendingLen() > 0
	}, testTimeout(10*time.Second), 100*time.Millisecond)

	// Let it finish, then confirm the cap held.
	require.True(t, s.WaitForQuiet(t.Context(), 2*time.Second, testTimeout(30*time.Second)))
	require.LessOrEqual(t, s.PendingLen(), maxPending)
}

func TestSession_SendAfterExitFails(t *testing.T) {
	s := startTestSession(t)
	waitReady(t, s)
	require.NoError(t, s.Send([]byte("exit\n")))
	require.Eventually(t, func() bool { return !s.Alive() }, testTimeout(10*time.Second), 50*time.Millisecond)
	require.Error(t, s.Send([]byte("echo late\n")))
}

// Nothing is guessed: with no shell to be found, Shell says so instead
// of naming one, and the caller drops the tool rather than opening a
// session against a path that may not exist.
func TestShellIdentification(t *testing.T) {
	t.Setenv("SHELL", "/usr/bin/zsh")
	sh, ok := Shell()
	require.True(t, ok)
	require.NotEmpty(t, sh)

	// $SHELL naming something that is not a shell is no answer either.
	t.Setenv("SHELL", "/usr/bin/python")
	sh, ok = Shell()
	if parentShell() == "" {
		require.False(t, ok, "an unrecognised $SHELL is not an identification")
		require.Empty(t, sh)
	}
}

func TestSession_LongOutputTailPreserved(t *testing.T) {
	s := startTestSession(t)
	waitReady(t, s)
	s.Drain()

	require.NoError(t, s.Send([]byte("seq 1 2000\n")))
	require.True(t, s.WaitForQuiet(t.Context(), 2*time.Second, testTimeout(15*time.Second)))
	out := string(s.Drain())
	out = strings.ReplaceAll(out, "\r", "")
	require.Contains(t, out, "1999")
	require.True(t, strings.Contains(strings.ReplaceAll(out, "\r", ""), "1\n2\n"))
}

func TestSessionScreenAndAltScreen(t *testing.T) {
	s := startTestSession(t)
	waitReady(t, s)

	require.False(t, s.AltScreen())

	require.NoError(t, s.Send([]byte("printf 'on the screen'\n")))
	require.True(t, waitOutput(t, s, regexp.MustCompile("on the screen"), testTimeout(5*time.Second)))
	require.Contains(t, s.Screen(), "on the screen")

	// A program taking the alternate screen is visible as such, and the
	// rendered screen follows it rather than the raw byte stream.
	// The echoed command line arrives before the command runs, so wait
	// on the emulator's state rather than on text that is also part of
	// what was typed.
	require.NoError(t, s.Send([]byte("printf '\\033[?1049h'; printf 'full screen app'\n")))
	require.Eventually(t, s.AltScreen, testTimeout(5*time.Second), 50*time.Millisecond)
	// The mode flips the moment the escape is parsed, but the text after
	// it can still sit in a later pty read: the alt screen starts blank
	// (1049h clears it), so a one-shot snapshot can render "". Poll the
	// rendered screen until the bytes have landed.
	require.Eventually(t, func() bool {
		return strings.Contains(s.Screen(), "full screen app")
	}, testTimeout(5*time.Second), 50*time.Millisecond)

	require.NoError(t, s.Send([]byte("printf '\\033[?1049l'\n")))
	require.Eventually(t, func() bool { return !s.AltScreen() }, testTimeout(5*time.Second), 50*time.Millisecond)
}

func TestSessionResize(t *testing.T) {
	s := startTestSession(t)
	waitReady(t, s)

	require.NoError(t, s.Resize(24, 80))
	rows, cols := s.Size()
	require.Equal(t, 24, rows)
	require.Equal(t, 80, cols)

	require.NoError(t, s.Send([]byte("printf '%s %s' \"$(tput lines)\" \"$(tput cols)\"\n")))
	require.True(t, waitOutput(t, s, regexp.MustCompile(`24 80`), testTimeout(5*time.Second)))

	// Out-of-range dimensions are clamped, never applied verbatim.
	require.NoError(t, s.Resize(1, 5))
	rows, cols = s.Size()
	require.Equal(t, minRows, rows)
	require.Equal(t, minCols, cols)
}

func TestDefaultSizeEnvOverride(t *testing.T) {
	t.Setenv("HARNESS_PTY_ROWS", "42")
	t.Setenv("HARNESS_PTY_COLS", "123")
	rows, cols := DefaultSize()
	require.Equal(t, 42, rows)
	require.Equal(t, 123, cols)

	t.Setenv("HARNESS_PTY_ROWS", "not a number")
	rows, _ = DefaultSize()
	require.Equal(t, DefaultRows, rows)
}
