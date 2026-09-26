package tools

import (
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

	res, err := r.Type(t.Context(), "nvim -u NONE", 15)
	require.NoError(t, err)
	require.True(t, res.AltScreen, "editor should own the screen")
	require.True(t, res.Running)
	require.Nil(t, res.ExitCode, "a running editor has no exit code")

	res, err = r.Type(t.Context(), "ihello from the harness", 10)
	require.NoError(t, err)
	require.Contains(t, res.Output, "hello from the harness", "typed text should appear on the rendered screen")

	// Polling an idle screen reports it as unchanged instead of
	// resending it.
	poll, err := r.Poll(t.Context())
	require.NoError(t, err)
	require.True(t, poll.Unchanged)
	require.Empty(t, poll.Output)

	res, err = r.Type(t.Context(), "\x1b:q!\r", 10)
	require.NoError(t, err)
	require.False(t, res.AltScreen, "editor should have quit")

	done, err := r.Type(t.Context(), "echo back", 10)
	require.NoError(t, err)
	require.NotNil(t, done.ExitCode)
	require.Equal(t, "back", done.Output, "the shell is back at a prompt with clean output")
}

func TestPtyRunner_PagerRendersAndQuits(t *testing.T) {
	requireProgram(t, "less")
	r := newTestRunner(t)

	res, err := r.Type(t.Context(), "seq 1 500 | less", 10)
	require.NoError(t, err)
	require.True(t, res.AltScreen)
	require.Contains(t, res.Output, "1\n2\n3")

	res, err = r.Type(t.Context(), "q", 10)
	require.NoError(t, err)
	require.False(t, res.AltScreen)

	done, err := r.Type(t.Context(), "echo back", 10)
	require.NoError(t, err)
	require.Equal(t, "back", done.Output)
}

// A nested interactive shell enables bracketed paste without taking the
// alternate screen - the other half of the old false "command finished".
func TestPtyRunner_NestedInteractiveShell(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Type(t.Context(), "sh -i", 5)
	require.NoError(t, err)
	require.True(t, res.Running)
	require.Nil(t, res.ExitCode)

	res, err = r.Type(t.Context(), "echo nested\n", 10)
	require.NoError(t, err)
	require.Contains(t, res.Output, "nested")

	_, err = r.Type(t.Context(), "\x04", 10)
	require.NoError(t, err)

	done, err := r.Type(t.Context(), "echo back", 10)
	require.NoError(t, err)
	require.Equal(t, "back", done.Output)
}

// The session opens at the configured size; a program reads it once,
// on startup, and there is no resizing after that.
func TestPtyRunner_OpensAtConfiguredSize(t *testing.T) {
	t.Setenv("HARNESS_PTY_ROWS", "30")
	t.Setenv("HARNESS_PTY_COLS", "100")
	r := newTestRunner(t)

	rows, cols := r.Size()
	require.Equal(t, 30, rows)
	require.Equal(t, 100, cols)

	// stty size reads the window size straight from the terminal; tput
	// would consult terminfo and any inherited LINES/COLUMNS instead.
	res, err := r.Type(t.Context(), "stty size", 10)
	require.NoError(t, err)
	require.Equal(t, "30 100", strings.TrimSpace(res.Output))
}

func TestKeyChunks(t *testing.T) {
	t.Parallel()

	// A lone escape is its own chunk, so what follows cannot read as
	// its meta suffix.
	require.Equal(t, []string{"\x1b", ":q!\r"}, keyChunks("\x1b:q!\r"))

	// A complete escape sequence stays one chunk; the pacing gap lands
	// after it, never inside it.
	require.Equal(t, []string{"\x1b[A"}, keyChunks("\x1b[A"))
	require.Equal(t, []string{"\x1bOP"}, keyChunks("\x1bOP"))
	require.Equal(t, []string{"abc", "\x1b[B", "def"}, keyChunks("abc\x1b[Bdef"))

	// Text without escapes is one chunk and goes out in one write.
	require.Equal(t, []string{"a\nb\n"}, keyChunks("a\nb\n"))
	require.Nil(t, keyChunks(""))
}

func TestContainsKeyBytes(t *testing.T) {
	t.Parallel()

	// Newline is the documented enter, not a key: command text with
	// newlines still travels as one paste.
	require.False(t, containsKeyBytes("echo hi\n"))
	require.False(t, containsKeyBytes("cat <<EOF\nbody\nEOF\n"))

	// Angle brackets are literal text; nothing is parsed out of a
	// command, so a heredoc or message that says "press <enter>"
	// reaches the terminal with the word intact.
	require.False(t, containsKeyBytes("cat <file >out\n"))
	require.False(t, containsKeyBytes("press <enter> to continue"))

	require.True(t, containsKeyBytes("y\r"))
	require.True(t, containsKeyBytes("\x03"))
	require.True(t, containsKeyBytes("\x1b[A"))
	require.True(t, containsKeyBytes("\x7f"))
}

func TestEndsKeyed(t *testing.T) {
	t.Parallel()

	// Input already ending in a keystroke is typed as given; text gets
	// the implied enter.
	require.True(t, endsKeyed("\x1b:wq\r"))
	require.True(t, endsKeyed("\x1b[A"))
	require.True(t, endsKeyed("abc\t"))
	require.False(t, endsKeyed("abc"))
	require.False(t, endsKeyed(""))
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

// One session, used like a person's terminal: what is typed while an
// editor is open goes to the editor, whatever it looks like, and the
// shell is back the moment the editor is gone. The caller never says
// which of the two it means.
func TestPtyRunner_TypingGoesToWhateverIsRunning(t *testing.T) {
	requireProgram(t, "nvim")
	r := newTestRunner(t)

	res, err := r.Type(t.Context(), "nvim -u NONE", 15)
	require.NoError(t, err)
	require.True(t, res.AltScreen)

	// Looks like a command; is keystrokes for the editor.
	res, err = r.Type(t.Context(), "iecho beside", 10)
	require.NoError(t, err)
	require.True(t, res.AltScreen, "the editor still has the terminal")
	require.Contains(t, res.Output, "echo beside", "the text went into the buffer")

	// Quitting hands the terminal back, and the same text now runs.
	res, err = r.Type(t.Context(), "\x1b:q!\r", 10)
	require.NoError(t, err)
	require.False(t, res.AltScreen)
	done, err := r.Type(t.Context(), "echo beside", 10)
	require.NoError(t, err)
	require.NotNil(t, done.ExitCode)
	require.Equal(t, "beside", done.Output)
}

// Enter is implied at the prompt, so a command line needs no newline;
// one that carries its own is not run twice.
func TestPtyRunner_EnterImpliedAtPrompt(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Type(t.Context(), "echo one", 10)
	require.NoError(t, err)
	require.Equal(t, "one", res.Output)

	res, err = r.Type(t.Context(), "echo two\n", 10)
	require.NoError(t, err)
	require.Equal(t, "two", res.Output)
	require.NotNil(t, res.ExitCode)
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
		res, err := r.Type(t.Context(), "42\n", 10)
		if err != nil {
			t.Error(err)
		}
		answered <- res
	}()

	res, err := r.Type(t.Context(), `printf 'how many? '; read n; echo "answer:$n"`, 20)
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

	res, err := r.Type(t.Context(), "echo first; sleep 1; echo second", 20)
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
	r := newBracketedPasteRunner(t)

	// Bracketed paste is on at the prompt of an interactive shell.
	require.True(t, r.session.BracketedPaste(), "the shell should have bracketed paste on")

	res, err := r.Type(t.Context(), "printf 'a\\n'\nprintf 'b\\n'\n", 10)
	require.NoError(t, err)
	require.Contains(t, res.Output, "a")
	require.Contains(t, res.Output, "b")
}

func TestPtyRunner_SinglelineInputIsNotPasted(t *testing.T) {
	r := newTestRunner(t)

	_, err := r.Type(t.Context(), "read answer; echo \"got:$answer\"", 1)
	require.NoError(t, err)

	res, err := r.Type(t.Context(), "plain\n", 10)
	require.NoError(t, err)
	require.Contains(t, res.Output, "got:plain")
	require.NotContains(t, res.Output, "200~", "paste markers must not reach a program that did not ask for them")
}

// Reading a session while a command is running must not wait for that
// command to finish.
func TestPtyRunner_ReadsDoNotWaitForARunningCommand(t *testing.T) {
	r := newTestRunner(t)

	go func() {
		if _, err := r.Type(t.Context(), "sleep 8; echo done", 20); err != nil {
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
	_, err = r.Type(t.Context(), "\x03", 10)
	require.NoError(t, err)
}
