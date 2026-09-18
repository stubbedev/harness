package shell

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func skipIfNoSh(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this platform")
	}
}

// streamTimeouts shortens the watchdog bounds for the test and restores
// them after.
func streamTimeouts(t *testing.T, idle, max time.Duration) {
	t.Helper()
	oldIdle, oldMax := streamIdleTimeout, streamMaxRuntime
	streamIdleTimeout, streamMaxRuntime = idle, max
	t.Cleanup(func() { streamIdleTimeout, streamMaxRuntime = oldIdle, oldMax })
}

// TestRunAndCaptureStream_KillsSilentCommands: silence is not proof of
// life. A command that never speaks hits the idle watchdog, the run
// ends, and the output says why - the caller is never left waiting.
func TestRunAndCaptureStream_KillsSilentCommands(t *testing.T) {
	skipIfNoSh(t)
	streamTimeouts(t, 300*time.Millisecond, time.Minute)

	start := time.Now()
	result, err := RunAndCaptureStream(t.Context(), RunOptions{
		Command: "sleep 30",
		Cwd:     t.TempDir(),
	}, nil)
	require.NoError(t, err)
	require.Less(t, time.Since(start), 10*time.Second, "the watchdog must end the run long before sleep finishes")
	require.Contains(t, result.Output, "command killed by harness", "the output must say the run was killed")
	require.Contains(t, result.Output, "no output for")
}

// TestRunAndCaptureStream_AbsoluteCap: a chatty command cannot outlive
// the runtime bound even while streaming output the whole time.
func TestRunAndCaptureStream_AbsoluteCap(t *testing.T) {
	skipIfNoSh(t)
	streamTimeouts(t, time.Hour, 300*time.Millisecond)

	start := time.Now()
	result, err := RunAndCaptureStream(t.Context(), RunOptions{
		Command: "while true; do echo flood; done",
		Cwd:     t.TempDir(),
	}, nil)
	require.NoError(t, err)
	require.Less(t, time.Since(start), 10*time.Second)
	require.Contains(t, result.Output, "still running after")
}

// TestRunAndCaptureStream_FloodIsCapped: output past the capture cap is
// dropped from the head, the tail is kept, and the marker says how much
// went. Memory stays bounded no matter how much the command prints.
func TestRunAndCaptureStream_FloodIsCapped(t *testing.T) {
	skipIfNoSh(t)

	result, err := RunAndCaptureStream(t.Context(), RunOptions{
		Command: "yes 0123456789 | head -c 12000000",
		Cwd:     t.TempDir(),
	}, nil)
	require.NoError(t, err)
	require.Contains(t, result.Output, "earlier bytes dropped", "the cap must be reached and marked")
	require.LessOrEqual(t, len(result.Output), maxCaptureBytes+maxCaptureBytes/8+256,
		"the captured output must stay bounded")
	require.Contains(t, result.Output, "0123456789", "the tail is kept")
}

// TestRunAndCaptureStream_ProgressIsCoalesced: onProgress fires at most
// once per chunk interval however fast the output arrives.
func TestRunAndCaptureStream_ProgressIsCoalesced(t *testing.T) {
	skipIfNoSh(t)

	var pushes int
	done := make(chan struct{})
	_, _ = RunAndCaptureStream(t.Context(), RunOptions{
		Command: "yes 0123456789 | head -c 4000000",
		Cwd:     t.TempDir(),
	}, func(string) {
		pushes++
		if pushes == 1 {
			close(done)
		}
	})
	select {
	case <-done:
	default:
		t.Fatal("onProgress never fired")
	}
	require.Less(t, pushes, 100, "a multi-megabyte flood must not reach the consumer chunk by chunk")
}

// TestRunAndCaptureStream_CompletesNormally: nothing in the hardening
// changes an ordinary command's result.
func TestRunAndCaptureStream_CompletesNormally(t *testing.T) {
	skipIfNoSh(t)

	result, err := RunAndCaptureStream(t.Context(), RunOptions{
		Command: fmt.Sprintf("echo %s", strings.Repeat("x", 10)),
		Cwd:     t.TempDir(),
	}, nil)
	require.NoError(t, err)
	require.Equal(t, 0, result.ExitCode)
	require.Contains(t, result.Output, "xxxxxxxxxx")
	require.NotContains(t, result.Output, "killed")
}
