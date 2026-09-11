package tools

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func requireProgram(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not installed", name)
	}
}

// A full-screen program must be reported as running with its screen
// rendered - never as a finished command. The bug this guards against:
// nvim enables bracketed paste on startup, which the old prompt
// heuristic read as "the shell is back", so the exit-code sentinel was
// typed into the editor and the call sat there until it timed out.
func TestPtyRunner_EditorIsDrivenNotMistakenForAPrompt(t *testing.T) {
	requireProgram(t, "nvim")
	r := newTestRunner(t)

	res, err := r.Run(t.Context(), "nvim -u NONE", 15)
	require.NoError(t, err)
	require.True(t, res.AltScreen, "editor should own the screen")
	require.True(t, res.Running)
	require.Nil(t, res.ExitCode, "a running editor has no exit code")

	res, err = r.Input(t.Context(), "ihello from the harness")
	require.NoError(t, err)
	require.Contains(t, res.Output, "hello from the harness", "typed text should appear on the rendered screen")

	// Polling an idle screen reports it as unchanged instead of
	// resending it.
	poll, err := r.Poll(t.Context())
	require.NoError(t, err)
	require.True(t, poll.Unchanged)
	require.Empty(t, poll.Output)

	res, err = r.Keys(t.Context(), "escape, :, q, !, enter")
	require.NoError(t, err)
	require.False(t, res.AltScreen, "editor should have quit")

	done, err := r.Run(t.Context(), "echo back", 10)
	require.NoError(t, err)
	require.NotNil(t, done.ExitCode)
	require.Equal(t, "back", done.Output, "the shell is back at a prompt with clean output")
}

func TestPtyRunner_PagerRendersAndQuits(t *testing.T) {
	requireProgram(t, "less")
	r := newTestRunner(t)

	res, err := r.Run(t.Context(), "seq 1 500 | less", 10)
	require.NoError(t, err)
	require.True(t, res.AltScreen)
	require.Contains(t, res.Output, "1\n2\n3")

	res, err = r.Keys(t.Context(), "q")
	require.NoError(t, err)
	require.False(t, res.AltScreen)

	done, err := r.Run(t.Context(), "echo back", 10)
	require.NoError(t, err)
	require.Equal(t, "back", done.Output)
}

// A nested interactive shell enables bracketed paste without taking the
// alternate screen - the other half of the old false "command finished".
func TestPtyRunner_NestedInteractiveShell(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Run(t.Context(), "sh -i", 5)
	require.NoError(t, err)
	require.True(t, res.Running)
	require.Nil(t, res.ExitCode)

	res, err = r.Input(t.Context(), "echo nested\n")
	require.NoError(t, err)
	require.Contains(t, res.Output, "nested")

	_, err = r.Keys(t.Context(), "ctrl+d")
	require.NoError(t, err)

	done, err := r.Run(t.Context(), "echo back", 10)
	require.NoError(t, err)
	require.Equal(t, "back", done.Output)
}

func TestPtyRunner_Resize(t *testing.T) {
	r := newTestRunner(t)

	_, err := r.Resize(t.Context(), 30, 100)
	require.NoError(t, err)
	rows, cols := r.Size()
	require.Equal(t, 30, rows)
	require.Equal(t, 100, cols)

	res, err := r.Run(t.Context(), "printf '%s %s' \"$(tput lines)\" \"$(tput cols)\"", 10)
	require.NoError(t, err)
	require.Equal(t, "30 100", strings.TrimSpace(res.Output))
}

func TestParseKeys(t *testing.T) {
	t.Parallel()

	keys, err := parseKeys("escape, :, q, enter")
	require.NoError(t, err)
	require.Equal(t, [][]byte{[]byte("\x1b"), []byte(":"), []byte("q"), []byte("\r")}, keys)

	keys, err = parseKeys("CTRL+C")
	require.NoError(t, err)
	require.Equal(t, [][]byte{{0x03}}, keys)

	_, err = parseKeys("banana")
	require.Error(t, err)

	_, err = parseKeys("  ")
	require.Error(t, err)
}

func TestParseTerminalSize(t *testing.T) {
	t.Parallel()

	rows, cols, err := parseTerminalSize("240x60")
	require.NoError(t, err)
	require.Equal(t, 60, rows)
	require.Equal(t, 240, cols)

	_, _, err = parseTerminalSize("wide")
	require.Error(t, err)
}

func TestEchoedLine(t *testing.T) {
	t.Parallel()

	require.True(t, echoedLine("echo hi", "echo hi"))
	require.True(t, echoedLine("sh-5.3$ echo hi", "echo hi"))
	// Real output that merely ends with what was sent is kept.
	require.False(t, echoedLine("got:hello", "hello"))
}

func TestTruncateOutputKeepsMoreTailThanHead(t *testing.T) {
	t.Parallel()

	head := strings.Repeat("H", MaxOutputLength)
	tail := strings.Repeat("T", MaxOutputLength)
	out := TruncateOutput(head + tail)

	// The end of a build log is where the error is, so the tail budget
	// is the larger one.
	require.Greater(t, strings.Count(out, "T"), strings.Count(out, "H")*3)
	require.Contains(t, out, "lines truncated")
}

// An editor holding one session must not stop the next command: it runs
// in a second shell for the same directory, and keystrokes still reach
// the editor.
func TestPtySessions_CommandRunsBesideAnInteractiveProgram(t *testing.T) {
	requireProgram(t, "nvim")
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this platform")
	}
	t.Setenv("SHELL", "/bin/sh")

	dir := t.TempDir()
	ptyRunnersMu.Lock()
	saved := ptyRunners
	ptyRunners = map[string]*ptyRunner{}
	ptyRunnersMu.Unlock()
	t.Cleanup(func() {
		ptyRunnersMu.Lock()
		for _, r := range ptyRunners {
			r.Close()
		}
		ptyRunners = saved
		ptyRunnersMu.Unlock()
	})

	// Open an editor in the primary session.
	primary, err := ptyCommandRunner(t.Context(), dir, nil)
	require.NoError(t, err)
	require.Equal(t, 0, primary.slot)
	res, err := primary.Run(t.Context(), "nvim -u NONE", 15)
	require.NoError(t, err)
	require.True(t, res.AltScreen)

	// A command now goes to a second session rather than being typed
	// into the editor.
	second, err := ptyCommandRunner(t.Context(), dir, nil)
	require.NoError(t, err)
	require.Equal(t, 1, second.slot)
	done, err := second.Run(t.Context(), "echo beside", 10)
	require.NoError(t, err)
	require.NotNil(t, done.ExitCode)
	require.Equal(t, "beside", done.Output)

	// Keystrokes still find the editor, not the free shell.
	require.Same(t, primary, ptyInteractiveRunner(dir, nil))
	res, err = ptyInteractiveRunner(dir, nil).Input(t.Context(), "ibeside too")
	require.NoError(t, err)
	require.Contains(t, res.Output, "beside too")

	// With the editor gone, commands go back to the primary session.
	_, err = primary.Keys(t.Context(), "escape, :, q, !, enter")
	require.NoError(t, err)
	back, err := ptyCommandRunner(t.Context(), dir, nil)
	require.NoError(t, err)
	require.Equal(t, 0, back.slot)
}

// A command that stops to ask something must be answerable while it is
// still running: the keystroke call cannot wait for the command it is
// meant to unblock.
func TestPtyRunner_AnswerAPromptWhileTheCommandIsRunning(t *testing.T) {
	r := newTestRunner(t)

	answered := make(chan PTYResult, 1)
	go func() {
		// Give the command a moment to reach its prompt, then answer it
		// from a second call while the first is still waiting.
		time.Sleep(750 * time.Millisecond)
		res, err := r.Input(t.Context(), "42\n")
		if err != nil {
			t.Error(err)
		}
		answered <- res
	}()

	res, err := r.Run(t.Context(), `printf 'how many? '; read n; echo "answer:$n"`, 20)
	require.NoError(t, err)
	require.NotNil(t, res.ExitCode, "the command completed once it was answered")
	require.Contains(t, res.Output, "answer:42")

	// The call that typed the answer returned on its own - it did not
	// sit behind the command it was unblocking - and it reported the
	// question on screen rather than claiming the command's output.
	select {
	case typed := <-answered:
		require.Contains(t, typed.Output, "how many?")
	case <-time.After(5 * time.Second):
		t.Fatal("the input call never returned")
	}
}

// Polling a session that is mid-command must not consume the output the
// waiting call is going to report.
func TestPtyRunner_PollDoesNotStealARunningCommandsOutput(t *testing.T) {
	r := newTestRunner(t)

	polled := make(chan PTYResult, 1)
	go func() {
		time.Sleep(500 * time.Millisecond)
		res, err := r.Poll(t.Context())
		if err != nil {
			t.Error(err)
		}
		polled <- res
	}()

	res, err := r.Run(t.Context(), "echo first; sleep 1; echo second", 20)
	require.NoError(t, err)
	require.Contains(t, res.Output, "first")
	require.Contains(t, res.Output, "second", "the poll must not have drained this")

	select {
	case p := <-polled:
		require.True(t, p.WhileBusy)
	case <-time.After(5 * time.Second):
		t.Fatal("the poll never returned")
	}
}

// Several lines typed into a shell run one at a time and fight with
// auto-indent. Delivered as a paste, they arrive as one block.
func TestPtyRunner_MultilineInputIsPasted(t *testing.T) {
	r := newTestRunner(t)

	// Bracketed paste is on at the prompt of an interactive shell.
	require.True(t, r.session.BracketedPaste(), "the shell should have bracketed paste on")

	res, err := r.Input(t.Context(), "printf 'a\\n'\nprintf 'b\\n'\n")
	require.NoError(t, err)
	require.Contains(t, res.Output, "a")
	require.Contains(t, res.Output, "b")
}

func TestPtyRunner_SinglelineInputIsNotPasted(t *testing.T) {
	r := newTestRunner(t)

	_, err := r.Run(t.Context(), "read answer; echo \"got:$answer\"", 1)
	require.NoError(t, err)

	res, err := r.Input(t.Context(), "plain\n")
	require.NoError(t, err)
	require.Contains(t, res.Output, "got:plain")
	require.NotContains(t, res.Output, "200~", "paste markers must not reach a program that did not ask for them")
}

// Reading a session while a command is running must not wait for that
// command to finish.
func TestPtyRunner_ReadsDoNotWaitForARunningCommand(t *testing.T) {
	r := newTestRunner(t)

	go func() {
		if _, err := r.Run(t.Context(), "sleep 8; echo done", 20); err != nil {
			t.Error(err)
		}
	}()
	time.Sleep(750 * time.Millisecond)

	start := time.Now()
	res, err := r.Poll(t.Context())
	require.NoError(t, err)
	require.Less(t, time.Since(start), 3*time.Second, "the poll waited for the command")
	require.True(t, res.WhileBusy)

	// Stop the sleeper so the session is clean for teardown.
	_, err = r.Keys(t.Context(), "ctrl+c")
	require.NoError(t, err)
}
