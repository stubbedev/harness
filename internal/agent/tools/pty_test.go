package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newTestRunner opens a runner over a real /bin/sh session in a temp
// directory: fast, deterministic, no rc files.
func newTestRunner(t *testing.T) *ptyRunner {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this platform")
	}
	return newRunnerWithShell(t, "/bin/sh")
}

// newBracketedPasteRunner opens a runner over a shell that asks for bracketed
// paste. Whether a shell asks is the shell's business, not ours -- /bin/sh is
// dash on Debian and bash 3.2 on macOS, and a bash built without readline asks
// for nothing either -- so try the shells this machine has and skip when none
// of them turns the mode on.
func newBracketedPasteRunner(t *testing.T) *ptyRunner {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this platform")
	}
	candidates := []string{"/bin/bash"}
	if p, err := exec.LookPath("bash"); err == nil {
		candidates = append(candidates, p)
	}
	candidates = append(candidates, "/bin/sh")

	for _, shell := range candidates {
		if _, err := os.Stat(shell); err != nil {
			continue
		}
		r := newRunnerWithShell(t, shell)
		if r.session.BracketedPaste() {
			return r
		}
		// Nil out the session so the runner's cleanup does not close it twice.
		r.session.Close()
		r.session = nil
	}
	t.Skip("no shell on this machine asks for bracketed paste")
	return nil
}

// newRunnerWithShell opens a runner over the given shell in a temp directory.
func newRunnerWithShell(t *testing.T, shell string) *ptyRunner {
	t.Helper()
	t.Setenv("SHELL", shell)
	r := &ptyRunner{cwd: t.TempDir()}
	t.Cleanup(func() {
		if r.session != nil {
			r.session.Close()
		}
	})
	// Open synchronously so tests observe the session directly. Go through
	// terminal() rather than ensureSessionLocked: the latter needs r.mu,
	// which the warm-start goroutine in ptyRunnerSlot also takes.
	_, err := r.terminal(t.Context())
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
	// The shell reports the directory it is actually in, which is the
	// resolved one: on macOS the temp directory lives under /var, a
	// symlink to /private/var, so comparing the two spellings literally
	// fails there while testing nothing about tracking the cd.
	require.Equal(t, resolved(t, sub), resolved(t, res.Cwd))
}

// resolved is a path with every symlink along it followed, so two
// spellings of the same directory compare equal.
func resolved(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	return real
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

// A command that stops to ask something is detected from the process
// state, not waited out to the budget: the call returns as waiting for
// input within a couple of seconds even with a minute of budget left.
//
// That detection reads the foreground job's state out of procfs, so it
// exists on Linux and nowhere else (see term.SampleJob). Everywhere
// else the call has nothing to detect with and falls back to its wait
// budget, reporting the command as still running - which is the
// behaviour asserted below, with a budget short enough to be worth
// waiting for.
func TestPtyRunner_RunReturnsWhenInputNeeded(t *testing.T) {
	r := newTestRunner(t)

	if runtime.GOOS != "linux" {
		res, err := r.Run(t.Context(), "read answer; echo \"got:$answer\"", 2)
		require.NoError(t, err)
		require.True(t, res.Running)
		require.Nil(t, res.ExitCode)

		done, err := r.Input(t.Context(), "hello\n")
		require.NoError(t, err)
		require.Contains(t, done.Output, "got:hello")
		return
	}

	start := time.Now()
	res, err := r.Run(t.Context(), "read answer; echo \"got:$answer\"", 60)
	require.NoError(t, err)
	require.Less(t, time.Since(start), 20*time.Second, "the call should return on the quiet window, not the budget")
	require.True(t, res.Running)
	require.True(t, res.Waiting)
	require.Nil(t, res.ExitCode)

	done, err := r.Input(t.Context(), "hello\n")
	require.NoError(t, err)
	require.Contains(t, done.Output, "got:hello")
}

// A command left running by a timed-out call becomes an orphan: polling
// waits for it to finish and reports its exit code and output, rather
// than returning an instant snapshot the caller has to keep re-taking.
func TestPtyRunner_PollWaitsForOrphanedCommand(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Run(t.Context(), "sleep 3; echo late", 1)
	require.NoError(t, err)
	require.True(t, res.Running)

	poll, err := r.Poll(t.Context())
	require.NoError(t, err)
	require.NotNil(t, poll.ExitCode, "the poll waited for the orphan to finish")
	require.Equal(t, 0, *poll.ExitCode)
	require.Contains(t, poll.Output, "late")
	require.False(t, poll.Running)
}

// A command that keeps producing output past its wait budget is making
// progress: the wait is leased forward and the call returns its real
// completion, not a still-running snapshot the agent would poll for.
func TestPtyRunner_StreamingCommandLeasesPastBudget(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Run(t.Context(), "for i in 1 2 3 4 5 6; do echo tick $i; sleep 0.4; done", 2)
	require.NoError(t, err)
	require.NotNil(t, res.ExitCode, "the lease should have carried the call to completion")
	require.Equal(t, 0, *res.ExitCode)
	require.False(t, res.Running)
	require.Contains(t, res.Output, "tick 6")
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
	// Multiline commands work like any other: the prompt marker comes
	// back when the whole thing has run.
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

func TestEchoDebris(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		line string
		echo string
		want bool
	}{
		{"exact", "echo hi", "echo hi", true},
		{"prompt prefix", "sh-5.3$ echo hi", "echo hi", true},
		// zle echoes the line, then the syntax-highlighting redisplay
		// reprints it on the same line.
		{"redraw, whole copy", "echo helloecho hello", "echo hello", true},
		{"redraw, partial copy", "echo helloecho", "echo hello", true},
		{"redraw, skipped runs", `which zsh; echo "SHELL=$SHELL"; ps -o comm= -p $PPIDwhich; echo "SHELL=$SHELL"; ps -o -p $PPID`, `which zsh; echo "SHELL=$SHELL"; ps -o comm= -p $PPID`, true},
		// Real output that merely echoes fragments of the command is kept.
		{"output shorter than command", "echo", "echo hello", false},
		{"output ending with sent text", "got:hello", "hello", false},
		{"output starting mid-command", "hello", "echo hello", false},
		{"repeated junk", "nananananananana", "banana", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, echoDebris(tc.line, tc.echo))
		})
	}
}

// zshPromptSp is what zsh prints before every prompt when the
// partial-line marker (PROMPT_SP) is on: a highlighted %, a space fill
// to the last column, then carriage returns that erase it again. Stripped
// of escapes it frames real output with "%" plus whitespace, at the
// start (the prompt before the command, undrained) and the end (the
// prompt after it).
func zshPromptSp(fill int) string {
	return "\x1b[1m\x1b[7m%\x1b[27m\x1b[1m\x1b[0m" + strings.Repeat(" ", fill) +
		"\r \r\r\x1b[0m\x1b[27m\x1b[24m\x1b[J\x1b]133;A\a\x1b[K\x1b[?1h\x1b=\x1b[?2004h"
}

// TestPtyCleanZshStream runs clean() over raw zsh byte streams captured
// from a real session: zle echoes a character, backspaces over it and
// redisplays the command with per-character color codes and
// cursor-forward skips, and prompts wrap output in PROMPT_SP markers.
// The cleaned output must be the command's output alone.
func TestPtyCleanZshStream(t *testing.T) {
	t.Parallel()

	r := &ptyRunner{sentinel: newSentinel()}

	cases := []struct {
		name string
		raw  string
		echo []string
		want string
	}{
		{
			name: "echo",
			raw: zshPromptSp(215) +
				"e\becho hello\x1b[10D\x1b[36me\x1b[36mc\x1b[36mh\x1b[36mo\x1b[39m\x1b[6C" +
				"\x1b[?1l\x1b>\x1b[?2004l\r\r\nhello\r\n" + zshPromptSp(215) +
				r.sentinel.cmd + "\r\n",
			echo: []string{"echo hello"},
			want: "hello",
		},
		{
			name: "partial line",
			raw: "p\bprintf %s partial-output\x1b[24D\x1b[36mp\x1b[36mr\x1b[36mi\x1b[36mn\x1b[36mt\x1b[36mf\x1b[39m \x1b[33m%\x1b[33ms\x1b[39m\x1b[15C" +
				"\x1b[?1l\x1b>\x1b[?2004l\r\r\npartial-output" + zshPromptSp(215) +
				r.sentinel.cmd + "\r\n",
			echo: []string{"printf %s partial-output"},
			want: "partial-output",
		},
		{
			name: "long command",
			raw: "l\bls /home/x/internal/shell/\x1b[26D\x1b[36ml\x1b[36ms\x1b[39m \x1b[4m/\x1b[4mh\x1b[4mo\x1b[4mm\x1b[4me\x1b[24m\x1b[?1l\x1b>\x1b[?2004l\r\r\n" +
				"background.go    shell.go\r\nstream.go         run.go\r\n" + zshPromptSp(215) +
				r.sentinel.cmd + "\r\n",
			echo: []string{"ls /home/x/internal/shell/"},
			want: "background.go    shell.go\nstream.go         run.go",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, r.clean(tc.raw, tc.echo))
		})
	}
}

// TestPtyRunner_ZshUserShell runs commands through a real zsh with the
// user's rc files loaded: zle line editing, zsh-syntax-highlighting
// redisplay and the PROMPT_SP partial-line marker all echo far more than
// the command. The cleaned output must still be the command's output
// alone - no doubled command, no "%" framing, no filler whitespace.
func TestPtyRunner_ZshUserShell(t *testing.T) {
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh not installed")
	}
	t.Setenv("SHELL", "zsh")
	r := &ptyRunner{cwd: t.TempDir()}
	t.Cleanup(r.Close)
	_, err := r.terminal(t.Context())
	require.NoError(t, err)

	res, err := r.Run(t.Context(), "echo hello-zsh", 15)
	require.NoError(t, err)
	require.NotNil(t, res.ExitCode)
	require.Equal(t, 0, *res.ExitCode)
	require.Equal(t, "hello-zsh", res.Output)

	res, err = r.Run(t.Context(), "printf %s partial-zsh", 15)
	require.NoError(t, err)
	require.Equal(t, "partial-zsh", res.Output)
}

// A multiline command written straight into a line editor is mangled:
// zle echoes each line as it arrives, then redisplays it, toggling
// bracketed paste around every prompt - raw echo debris (blank lines,
// bells, doubled fragments) that survives cleaning. Commands must be
// delivered as a paste instead, so the shell takes the whole block at
// once. The issue's case: a heredoc whose body is many lines of code.
func TestPtyRunner_ZshUserShellHeredoc(t *testing.T) {
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh not installed")
	}
	t.Setenv("SHELL", "zsh")
	r := &ptyRunner{cwd: t.TempDir()}
	t.Cleanup(r.Close)
	_, err := r.terminal(t.Context())
	require.NoError(t, err)

	cmd := "cat > file.txt <<'EOF'\npackage main\n\nfunc main() {}\nEOF\ncat file.txt"
	res, err := r.Run(t.Context(), cmd, 15)
	require.NoError(t, err)
	require.NotNil(t, res.ExitCode)
	require.Equal(t, 0, *res.ExitCode)
	require.Equal(t, "package main\n\nfunc main() {}", res.Output,
		"a heredoc's body goes into the file, not the output")
}

func TestResolveBackspaces(t *testing.T) {
	t.Parallel()

	require.Equal(t, "echo hello", resolveBackspaces("e\becho hello"))
	require.Equal(t, "", resolveBackspaces("x\b"))
	require.Equal(t, "ün", resolveBackspaces("üü\bn"))
	require.Equal(t, "no backspaces", resolveBackspaces("no backspaces"))
}

func TestPtySudoPromptPattern(t *testing.T) {
	t.Parallel()

	require.True(t, sudoPromptRe.MatchString("[sudo] password for stubbe: "))
	require.True(t, sudoPromptRe.MatchString("sudo password for root: "))
	require.False(t, sudoPromptRe.MatchString("some password for fun: "))
}

func TestPtySentinelParsing(t *testing.T) {
	t.Parallel()

	s := newSentinel()
	tag := strings.TrimSuffix(strings.TrimPrefix(s.cmd, "printf '__exit_"), `:%d@%s__' "$?" "$PWD"`)
	require.Len(t, tag, 16)

	require.True(t, s.loose.MatchString("__exit_"+tag+":0@"))
	require.True(t, s.parse.MatchString("__exit_"+tag+":12@/tmp/x__"))
	m := s.parse.FindStringSubmatch("__exit_" + tag + ":-1@/home__")
	require.Equal(t, "-1", m[1])
	require.Equal(t, "/home", m[2])

	// The echoed sentinel command must not match (format specifiers).
	require.False(t, s.parse.MatchString(s.cmd))
	require.False(t, s.loose.MatchString(s.cmd))

	// A marker printed by something else - another session, a log line -
	// is not this session's answer.
	other := newSentinel()
	require.False(t, s.parse.MatchString("__exit_deadbeefdeadbeef:0@/tmp__"))
	require.False(t, s.parse.MatchString(other.cmd))
	require.NotEqual(t, s.cmd, other.cmd)
}

func TestPtyRunnerIdleReapAndCap(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this platform")
	}
	t.Setenv("SHELL", "/bin/sh")

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

	// Fill to the cap.
	for i := range ptyMaxRunners {
		dir := t.TempDir()
		r := ptyRunnerFor(dir, nil)
		if _, err := r.terminal(t.Context()); err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
	}
	ptyRunnersMu.Lock()
	require.Len(t, ptyRunners, ptyMaxRunners)
	ptyRunnersMu.Unlock()

	// One more evicts the most idle.
	time.Sleep(10 * time.Millisecond)
	r := ptyRunnerFor(t.TempDir(), nil)
	_, err := r.terminal(t.Context())
	require.NoError(t, err)
	ptyRunnersMu.Lock()
	require.Len(t, ptyRunners, ptyMaxRunners)
	ptyRunnersMu.Unlock()

	// An idle-beyond-timeout runner is reaped on the next sweep.
	ptyRunnersMu.Lock()
	var victim *ptyRunner
	for _, cand := range ptyRunners {
		if cand != r {
			victim = cand
			break
		}
	}
	require.NotNil(t, victim)
	victim.mu.Lock()
	victim.lastUsed = time.Now().Add(-ptyIdleTimeout - time.Minute)
	victim.mu.Unlock()
	ptyRunnersMu.Unlock()

	ptyRunnersMu.Lock()
	ptyReap()
	ptyRunnersMu.Unlock()

	ptyRunnersMu.Lock()
	defer ptyRunnersMu.Unlock()
	require.Len(t, ptyRunners, ptyMaxRunners-1)
	_, stillThere := ptyRunners[r.cwd]
	require.True(t, stillThere, "the just-used runner must survive the reap")
}

// Output a call leaves behind belongs to that call. A backgrounded job
// that prints after its call returned, or a prompt the previous command
// left in the buffer, must not surface in the next command's result:
// before each command the session is fenced down to a known point.
func TestPtyRunner_EarlierOutputStaysOutOfTheNextCall(t *testing.T) {
	r := newTestRunner(t)

	_, err := r.Run(t.Context(), "(sleep 1; echo LATE) &", 10)
	require.NoError(t, err)

	// Let the backgrounded job print into the session while no call is
	// waiting on it.
	time.Sleep(1500 * time.Millisecond)

	res, err := r.Run(t.Context(), "echo second", 10)
	require.NoError(t, err)
	require.NotNil(t, res.ExitCode)
	require.Equal(t, 0, *res.ExitCode)
	require.Equal(t, "second", res.Output, "a command reports its own output and nothing else")
}

// Reset is the way out of a session that cannot be talked down: the
// shell is killed and a fresh one takes its place, so everything the
// old one held is gone and commands work again.
func TestPtyRunner_ResetStartsAFreshShell(t *testing.T) {
	r := newTestRunner(t)

	_, err := r.Run(t.Context(), "export PTY_RESET_VAR=before", 10)
	require.NoError(t, err)

	require.NoError(t, r.Reset(t.Context()))

	res, err := r.Run(t.Context(), "printf %s \"$PTY_RESET_VAR\"", 10)
	require.NoError(t, err)
	require.Equal(t, "", res.Output, "the new shell has none of the old one's state")

	alive, err := r.Run(t.Context(), "echo alive", 10)
	require.NoError(t, err)
	require.Equal(t, "alive", alive.Output)
	require.NotNil(t, alive.ExitCode)
	require.Equal(t, 0, *alive.ExitCode)
}

// A command left running does not survive a reset: killing the shell is
// the point, and the session comes back usable rather than busy.
func TestPtyRunner_ResetClearsARunningCommand(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Run(t.Context(), "sleep 30", 1)
	require.NoError(t, err)
	require.True(t, res.Running)

	require.NoError(t, r.Reset(t.Context()))
	require.False(t, r.occupied(), "a reset session is free")

	done, err := r.Run(t.Context(), "echo back", 10)
	require.NoError(t, err)
	require.Equal(t, "back", done.Output)
}

// A shell without a line editor - dash, or bash built without readline -
// sends no bracketed-paste marker before its prompt, so the only thing
// separating a command's output from the prompt behind it is the prompt
// marker itself. Output that did not end in a newline must survive
// that: before the marker was split on, it glued onto the echoed exit
// sentinel, matched the sentinel, and was dropped with it.
func TestPtyRunner_CleanKeepsUnterminatedOutputWithoutBracketedPaste(t *testing.T) {
	r := &ptyRunner{cwd: t.TempDir()}
	r.sentinel = newSentinel()
	r.promptRe = ptyPromptRe

	cmd := `printf %s "$PTY_TEST_VAR"`
	// What a paste-less shell puts on the wire: the echoed command, the
	// output with no trailing newline, the prompt marker, then the echo
	// of the sentinel and its answer.
	// collectResult cuts the sentinel answer off before cleaning, so
	// what arrives here ends with the echo of the sentinel command.
	raw := cmd + "\r\npersisted" + "\x1b]133;A\x07" + r.sentinel.cmd + "\r\n"

	require.Equal(t, "persisted", r.clean(raw, []string{cmd}))
}

func TestPtyRunner_CleanDropsPromptAndSentinelLines(t *testing.T) {
	r := &ptyRunner{cwd: t.TempDir()}
	r.sentinel = newSentinel()
	r.promptRe = ptyPromptRe

	cmd := "echo hello"
	raw := cmd + "\r\nhello\r\n\x1b]133;A\x07" + r.sentinel.cmd + "\r\n" +
		"\x1b]133;A\x07" + r.sentinel.begin + "\r\n"

	require.Equal(t, "hello", r.clean(raw, []string{cmd}),
		"prompt markers, the exit sentinel and the fence are bookkeeping, not output")
}
