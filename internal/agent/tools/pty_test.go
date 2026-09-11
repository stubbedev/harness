package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// newTestRunner opens a runner over a real /bin/sh session in a temp
// directory: fast, deterministic, no rc files.
func newTestRunner(t *testing.T) *ptyRunner {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this platform")
	}
	t.Setenv("SHELL", "/bin/sh")
	r := &ptyRunner{cwd: t.TempDir()}
	t.Cleanup(func() {
		if r.session != nil {
			r.session.Close()
		}
	})
	// Open synchronously so tests observe the session directly.
	_, err := r.ensureSessionLocked(t.Context())
	require.NoError(t, err)
	return r
}

func TestPtyRunner_RunEcho(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Run(t.Context(), "echo hello", 10)
	require.NoError(t, err)
	require.NotNil(t, res.ExitCode)
	require.Equal(t, 0, *res.ExitCode)
	require.Equal(t, "hello", res.Output)
	require.False(t, res.Running)
	require.NotEmpty(t, res.Cwd)
}

func TestPtyRunner_RunNonzeroExit(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Run(t.Context(), "false", 10)
	require.NoError(t, err)
	require.NotNil(t, res.ExitCode)
	require.Equal(t, 1, *res.ExitCode)
}

func TestPtyRunner_StatePersistsAcrossCalls(t *testing.T) {
	r := newTestRunner(t)

	_, err := r.Run(t.Context(), "export PTY_TEST_VAR=persisted", 10)
	require.NoError(t, err)

	res, err := r.Run(t.Context(), "printf %s \"$PTY_TEST_VAR\"", 10)
	require.NoError(t, err)
	require.Equal(t, "persisted", res.Output)
}

func TestPtyRunner_CwdTracks(t *testing.T) {
	r := newTestRunner(t)

	sub := filepath.Join(r.cwd, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	res, err := r.Run(t.Context(), "cd sub", 10)
	require.NoError(t, err)
	require.Equal(t, sub, res.Cwd)
}

func TestPtyRunner_StillRunning(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Run(t.Context(), "sleep 5", 1)
	require.NoError(t, err)
	require.True(t, res.Running)
	require.Nil(t, res.ExitCode)

	// Interrupt the sleeper so the session is clean for teardown.
	_, err = r.Input(t.Context(), "\x03")
	require.NoError(t, err)
}

func TestPtyRunner_InputAnswersPrompt(t *testing.T) {
	r := newTestRunner(t)

	// A program reading stdin hangs a pipe-based runner; in the
	// terminal it just waits until Input feeds it.
	_, err := r.Run(t.Context(), "read answer; echo \"got:$answer\"", 1)
	require.NoError(t, err) // returns as still running

	res, err := r.Input(t.Context(), "hello\n")
	require.NoError(t, err)
	require.Contains(t, res.Output, "got:hello")

	// Verify the session is back at a prompt.
	done, err := r.Run(t.Context(), "true", 10)
	require.NoError(t, err)
	require.NotNil(t, done.ExitCode)
}

func TestPtyRunner_MultilineCommand(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Run(t.Context(), "for i in 1 2 3\ndo echo $i\ndone", 10)
	require.NoError(t, err)
	// Multiline commands run without a sentinel: no exit code, but
	// the output is still captured and cleaned.
	require.Contains(t, res.Output, "1")
	require.Contains(t, res.Output, "3")
}

func TestPtyRunner_LongOutputKeepsTail(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Run(t.Context(), "seq 1 500", 15)
	require.NoError(t, err)
	require.NotNil(t, res.ExitCode)
	require.Contains(t, res.Output, "499")
	require.Contains(t, res.Output, "500")
}

func TestPtyRunner_EchoStripped(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Run(t.Context(), "echo distinctivestring", 10)
	require.NoError(t, err)
	// The echoed command line is stripped; only the output remains.
	require.Equal(t, "distinctivestring", res.Output)
	require.Equal(t, 1, len(regexp.MustCompile("distinctivestring").FindAllString(res.Output, -1)))
}

func TestPtySudoPromptPattern(t *testing.T) {
	t.Parallel()

	require.True(t, sudoPromptRe.MatchString("[sudo] password for stubbe: "))
	require.True(t, sudoPromptRe.MatchString("sudo password for root: "))
	require.False(t, sudoPromptRe.MatchString("some password for fun: "))
}

func TestPtySentinelParsing(t *testing.T) {
	t.Parallel()

	require.True(t, ptySentinelLoose.MatchString("__exit:0@"))
	require.True(t, ptySentinelRe.MatchString("__exit:12@/tmp/x__"))
	m := ptySentinelRe.FindStringSubmatch("__exit:-1@/home__")
	require.Equal(t, "-1", m[1])
	require.Equal(t, "/home", m[2])

	// The echoed sentinel command must not match (format specifiers).
	require.False(t, ptySentinelRe.MatchString(ptySentinelCmd))
	require.False(t, ptySentinelLoose.MatchString(ptySentinelCmd))
}
