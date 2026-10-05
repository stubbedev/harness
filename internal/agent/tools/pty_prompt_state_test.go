package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newHookRunner opens a runner over a shell whose prompt-state hook
// installs - bash or zsh, whichever this machine has. The hook is the
// shell's own account of its prompts, and every reconciliation below
// reads it; /bin/sh (dash on Debian, bash elsewhere) is not assumed to
// have one.
func newHookRunner(t *testing.T) *ptyRunner {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("pty sessions are unsupported on windows")
	}
	for _, shell := range []string{"/bin/bash", "/usr/bin/zsh", "/bin/zsh"} {
		if _, err := os.Stat(shell); err != nil {
			continue
		}
		r := newRunnerWithShell(t, shell)
		// The first prompt after setup writes the hook's first line.
		// Waiting here rather than asserting outright keeps a slow
		// shell startup from reading as "no hook".
		for range 100 {
			if _, ok := r.readPromptState(); ok {
				return r
			}
			time.Sleep(20 * time.Millisecond)
		}
		r.session.Close()
		r.session = nil
		t.Fatalf("shell %s accepted the setup but its prompt hook never wrote state", shell)
	}
	t.Skip("no shell with a prompt hook on this machine")
	return nil
}

// The prompt-state hook is the shell's own account: a line per prompt,
// ordinal first, advancing past every command, and naming the directory
// the command left the session in. The exit code it carries is
// superseded by the sentinel on the wire almost immediately - the
// sentinel's own prompt rewrites the file - so the ordinal and the
// directory are what a later observer can still trust.
func TestPtyRunner_PromptStateAdvances(t *testing.T) {
	r := newHookRunner(t)

	before, ok := r.readPromptState()
	require.True(t, ok, "the setup's own prompt already wrote state")
	require.GreaterOrEqual(t, before.seq, 1)

	sub := filepath.Join(r.cwd, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	res, err := r.Type(t.Context(), "cd sub", 10)
	require.NoError(t, err)
	require.NotNil(t, res.ExitCode)

	after, ok := r.readPromptState()
	require.True(t, ok)
	require.Greater(t, after.seq, before.seq, "a completed command advanced the prompt ordinal")
	require.Equal(t, resolved(t, sub), resolved(t, after.cwd))
}

// The completion a call never watched still lands: a command that
// finishes between calls, whose prompt marker is then swallowed whole -
// here by draining the session the way a credential answer or a
// replaced prompt framework does in the wild - is recovered by the
// shell's own account of its prompts. The next poll collects the exit
// code instead of describing a program that no longer exists, which is
// the two-hour "still running, idle" wedge of the wild report.
func TestPtyRunner_DrainedCompletionStillCollects(t *testing.T) {
	r := newHookRunner(t)

	started, err := r.Type(t.Context(), "sleep 2; echo done", 1)
	require.NoError(t, err)
	require.True(t, started.Running)

	time.Sleep(3 * time.Second)
	r.session.Drain()

	poll, err := r.PollFor(t.Context(), 10)
	require.NoError(t, err)
	require.False(t, poll.Waiting, "an idle shell at its prompt is not a question")
	require.False(t, poll.Running, "a command the shell says finished is not running")
	require.NotNil(t, poll.ExitCode, "the recovered completion carries its exit code")
	require.Equal(t, 0, *poll.ExitCode)

	after, err := r.Type(t.Context(), "echo after", 10)
	require.NoError(t, err)
	require.Equal(t, "after", after.Output)
	require.NotNil(t, after.ExitCode)
}

// The queue behind a missed completion drains: a line queued while a
// command held the session runs in the call that reconciles the
// completion, instead of starving behind a program that already exited
// unseen - the queue starvation of the wild report.
func TestPtyRunner_QueuedLinesRunAfterDrainedCompletion(t *testing.T) {
	r := newHookRunner(t)

	started, err := r.Type(t.Context(), "sleep 2", 1)
	require.NoError(t, err)
	require.True(t, started.Running)

	queued, err := r.Type(t.Context(), "echo queued-line", 5)
	require.NoError(t, err)
	require.True(t, queued.Queued)

	time.Sleep(3 * time.Second)
	r.session.Drain()

	poll, err := r.PollFor(t.Context(), 15)
	require.NoError(t, err)
	require.False(t, poll.Running)
	require.Contains(t, poll.Output, "queued-line")
	require.NotNil(t, poll.ExitCode)
}

// An idle session is not running anything: a poll of a shell sitting at
// its prompt reports no program, where it used to report the shell's
// own liveness as a running command - the stale "still running, idle"
// a fresh shell was handed in the wild report.
func TestPtyRunner_IdlePollReportsNotRunning(t *testing.T) {
	r := newTestRunner(t)

	res, err := r.Type(t.Context(), "echo hi", 10)
	require.NoError(t, err)
	require.NotNil(t, res.ExitCode)

	poll, err := r.PollFor(t.Context(), 1)
	require.NoError(t, err)
	require.False(t, poll.Running, "a shell at its prompt is not a running command")
	require.Empty(t, poll.Output)
}

// A command that holds the terminal and shows nothing is counted: the
// first calls say no more than the state, and the escalations that
// follow name the wedge and the way out instead of confirming the
// caller's wrong mental model a fourth time - the running verdict's
// share of the streak the waiting one already had.
func TestPtyRunner_RunningIdleStreakEscalates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the silent-command verdict needs the foreground's job state")
	}
	r := newTestRunner(t)

	res, err := r.Type(t.Context(), "sleep 30", 1)
	require.NoError(t, err)
	require.True(t, res.Running)
	require.Empty(t, res.Output)
	require.Equal(t, 1, res.RunStreak)

	for streak := 2; streak <= ptyStuckEscalateCalls; streak++ {
		res, err = r.PollFor(t.Context(), 1)
		require.NoError(t, err)
		require.True(t, res.Running, "the sleeper holds the session")
		require.Empty(t, res.Output)
		require.Equal(t, streak, res.RunStreak)
	}
	require.True(t, res.runEscalated())
	require.Contains(t, runningHeader(res), "wedged")

	_, err = r.Type(t.Context(), "\x03", 10)
	require.NoError(t, err)
}

// A reset that throws queued lines away says how many: the queue
// promised they would run, and the result that breaks the promise
// carries the count instead of dropping it silently.
func TestPtyRunner_ResetReportsDroppedQueue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("queueing needs the foreground's input state, unobservable over ConPTY")
	}
	r := newTestRunner(t)

	started, err := r.Type(t.Context(), "sleep 30", 1)
	require.NoError(t, err)
	require.True(t, started.Running)
	for _, line := range []string{"echo first-queued", "echo second-queued"} {
		queued, err := r.Type(t.Context(), line, 5)
		require.NoError(t, err)
		require.True(t, queued.Queued, line)
	}

	reset, err := r.Reset(t.Context())
	require.NoError(t, err)
	require.Equal(t, 2, reset.QueuedDropped)

	fresh, err := r.Type(t.Context(), "echo fresh", 10)
	require.NoError(t, err)
	require.Equal(t, "fresh", fresh.Output)
}

// The dialects that can hook the prompt do, and the one that cannot
// says so by leaving the hook empty rather than installing something
// inert: a session without a side channel knows it has none.
func TestDialectPromptHook(t *testing.T) {
	t.Parallel()

	require.Contains(t, posixDialect.promptHook, posixPromptFunc)
	require.Equal(t, []string{posixDialect.setupCmd, posixDialect.promptHook}, posixDialect.setupLines())

	require.Empty(t, powershellDialect.promptHook, "PowerShell folds the hook into its prompt function")
	require.Equal(t, []string{powershellDialect.setupCmd}, powershellDialect.setupLines())
	require.Contains(t, powershellDialect.setupCmd, ptyPromptStateEnv)

	require.Empty(t, cmdDialect.promptHook, "cmd.exe has no prompt hook to install")
	require.Equal(t, []string{cmdDialect.setupCmd}, cmdDialect.setupLines())
}

// The side channel survives torn reads the way the hook's truncate and
// rewrite produces them: an empty file or a partial line is no
// observation, and a session without a file never has one.
func TestReadPromptState(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	r := &ptyRunner{}
	_, ok := r.readPromptState()
	require.False(t, ok, "a session without a side channel observes nothing")

	r = &ptyRunner{promptFile: filepath.Join(dir, "state")}
	_, ok = r.readPromptState()
	require.False(t, ok, "a file that does not exist yet is no observation")

	for _, torn := range []string{"", "\n", "3\t0", "three\t0\t/tmp", "0\t0\t/tmp"} {
		require.NoError(t, os.WriteFile(r.promptFile, []byte(torn), 0o600))
		_, ok = r.readPromptState()
		require.False(t, ok, "torn state %q is no observation", torn)
	}

	require.NoError(t, os.WriteFile(r.promptFile, []byte("3\t0\t/tmp\n"), 0o600))
	st, ok := r.readPromptState()
	require.True(t, ok)
	require.Equal(t, promptState{seq: 3, code: 0, cwd: "/tmp"}, st)
}

// The baseline is marked from the file the command starts at, and a
// file that cannot be read disables the reconciliation rather than
// letting a stale ordinal call a running command finished.
func TestMarkPromptSeq(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")

	r := &ptyRunner{promptFile: missing}
	r.markPromptSeq()
	r.mu.Lock()
	require.Equal(t, -1, r.sentSeq, "an unreadable baseline disables the reconciliation")
	r.mu.Unlock()
	require.False(t, r.promptPastSent())

	r.promptFile = filepath.Join(dir, "state")
	require.NoError(t, os.WriteFile(r.promptFile, []byte("2\t0\t/tmp\n"), 0o600))
	r.markPromptSeq()
	r.mu.Lock()
	require.Equal(t, 2, r.sentSeq)
	r.mu.Unlock()
	require.False(t, r.promptPastSent(), "the prompt the command was sent at is not past it")

	require.NoError(t, os.WriteFile(r.promptFile, []byte("3\t0\t/tmp\n"), 0o600))
	require.True(t, r.promptPastSent(), "a later ordinal is the completion")
}

// The hook text is one line, and the POSIX pieces of it parse in every
// POSIX shell - a sourced setup file stops at a line the shell cannot
// parse, so a registration one shell ignores must still be readable by
// the rest.
func TestPosixPromptHookShape(t *testing.T) {
	t.Parallel()

	require.False(t, strings.ContainsAny(posixPromptHook, "\n"))
	for _, shell := range []string{"/bin/sh", "/bin/dash", "/bin/bash"} {
		if _, err := exec.LookPath(shell); err != nil {
			if _, statErr := os.Stat(shell); statErr != nil {
				continue
			}
		}
		cmd := exec.CommandContext(t.Context(), shell, "-n", "-c", posixPromptHook)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s rejected the hook: %s", shell, out)
	}
}
