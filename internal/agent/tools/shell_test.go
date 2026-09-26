package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func TestShellTool_DefaultAutoBackgroundThreshold(t *testing.T) {
	requireTerminalSession(t)
	workingDir := t.TempDir()
	tool := newShellToolForTest(t, workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runShellTool(t, tool, ctx, ShellParams{
		Command: "echo done",
	})

	require.False(t, resp.IsError)
	var meta ShellResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Contains(t, meta.Output, "done")
}

// A bare sleep burns the call's wall-clock while waiting for nothing:
// the call is refused with the way out (poll empty; wait on conditions
// inside the command that checks them), and a sleep inside a compound
// command still runs.
func TestShellTool_BareSleepIsRefused(t *testing.T) {
	requireTerminalSession(t)
	workingDir := t.TempDir()
	tool := newShellToolForTest(t, workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runShellTool(t, tool, ctx, ShellParams{
		Command: "sleep 5",
	})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "an empty call waits")

	resp = runShellTool(t, tool, ctx, ShellParams{
		Command: "sleep 0.1 && echo done",
	})
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "done")
}

// A single agent juggles named sessions: a long-lived program holds one
// terminal while another keeps serving commands, and the program's exit
// frees its session for new commands.
func TestShellTool_NamedSessionsJuggle(t *testing.T) {
	requireTerminalSession(t)
	workingDir := t.TempDir()
	tool := newShellToolForTest(t, workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	// A long-lived reader occupies the "srv" terminal.
	resp := runShellTool(t, tool, ctx, ShellParams{
		Session: "srv",
		Command: "cat",
	})
	require.False(t, resp.IsError)

	// Meanwhile the main terminal keeps serving commands.
	resp = runShellTool(t, tool, ctx, ShellParams{
		Command: "echo main-side",
	})
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "main-side")

	// The server terminal feeds its program and shows what it prints.
	resp = runShellTool(t, tool, ctx, ShellParams{
		Session: "srv",
		Command: "hello\r",
	})
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "hello")

	// End the program; its session is free for new commands again.
	resp = runShellTool(t, tool, ctx, ShellParams{
		Session: "srv",
		Command: "\x04",
	})
	require.False(t, resp.IsError)
	resp = runShellTool(t, tool, ctx, ShellParams{
		Session: "srv",
		Command: "echo srv-alive",
	})
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "srv-alive",
		"the session must be back at its shell once the program exits")

	var meta ShellResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Equal(t, "srv", meta.Session)
}

// A new session opens in the directory Harness was spawned from unless
// the call asks otherwise, and sessions keep independent state.
func TestShellTool_NewSessionsSpawnDirAndIsolation(t *testing.T) {
	requireTerminalSession(t)
	workingDir := t.TempDir()
	sub := filepath.Join(workingDir, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	tool := newShellToolForTest(t, workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runShellTool(t, tool, ctx, ShellParams{Session: "scratch", Command: "pwd"})
	require.False(t, resp.IsError)
	spawn, err := filepath.EvalSymlinks(workingDir)
	require.NoError(t, err)
	require.Contains(t, resp.Content, spawn, "a new session opens in the spawn directory")

	resp = runShellTool(t, tool, ctx, ShellParams{Session: "elsewhere", Command: "pwd", WorkingDir: sub})
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "sub", "working_dir places a new session")

	// State is per session: an export in one is invisible in another.
	resp = runShellTool(t, tool, ctx, ShellParams{Session: "scratch", Command: "export JUGGLE=a"})
	require.False(t, resp.IsError)
	resp = runShellTool(t, tool, ctx, ShellParams{Session: "elsewhere", Command: "printf %s \"$JUGGLE\""})
	require.False(t, resp.IsError)
	require.NotContains(t, resp.Content, "a", "sessions must not share shell state")
}

func TestShellTool_CustomAutoBackgroundThreshold(t *testing.T) {
	requireTerminalSession(t)
	workingDir := t.TempDir()
	tool := newShellToolForTest(t, workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	// auto_background_after is the wait budget: when it expires the
	// command stays alive in the persistent terminal session instead
	// of being moved to a background shell.
	resp := runShellTool(t, tool, ctx, ShellParams{
		Command:             "sleep 1.5 && echo done",
		AutoBackgroundAfter: 1,
	})

	require.False(t, resp.IsError)
	var meta ShellResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.NotContains(t, resp.Content, "moved to background")

	// A follow-up call in the same session observes later commands
	// finishing normally. If it arrives while the first command still
	// holds the session, it queues instead, and polls deliver it.
	resp = runShellTool(t, tool, ctx, ShellParams{
		Command: "echo after",
	})
	require.False(t, resp.IsError)
	for range 20 {
		if strings.Contains(resp.Content, "after") {
			break
		}
		resp = runShellTool(t, tool, ctx, ShellParams{})
		require.False(t, resp.IsError)
	}
	require.Contains(t, resp.Content, "after")
}

// newShellToolForTest builds a shell tool owned by the test, and closes
// the sessions it opened when the test ends: a shell that outlives the
// test holds the temp directory open, which Windows refuses to remove.
func newShellToolForTest(t *testing.T, workingDir string) fantasy.AgentTool {
	t.Helper()
	t.Cleanup(func() { closeOwnerSessions(t.Name()) })
	return NewShellTool(workingDir, t.Name(), nil)
}

// requireTerminalSession skips tests that execute commands through the
// persistent terminal session, which needs a PTY and cannot start on
// Windows.
func requireTerminalSession(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("terminal sessions are unsupported on Windows")
	}
}

func runShellTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params ShellParams) fantasy.ToolResponse {
	t.Helper()

	input, err := json.Marshal(params)
	require.NoError(t, err)

	call := fantasy.ToolCall{
		ID:    "test-call",
		Name:  ShellToolName,
		Input: string(input),
	}

	resp, err := tool.Run(ctx, call)
	require.NoError(t, err)
	return resp
}

func TestTruncateOutputValidUTF8(t *testing.T) {
	t.Parallel()
	// CJK characters are 2 cells wide; this string is far wider than
	// MaxOutputLength so TruncateOutput must truncate it.
	content := strings.Repeat("你好世界", MaxOutputLength)

	out := TruncateOutput(content)
	require.True(t, utf8.ValidString(out), "truncated output must stay valid UTF-8")
	require.Contains(t, out, "lines truncated")
}

func TestTruncateOutputShortContent(t *testing.T) {
	t.Parallel()
	content := "short output"
	require.Equal(t, content, TruncateOutput(content))
}

func TestTruncateOutputEmoji(t *testing.T) {
	t.Parallel()
	// Emoji with ZWJ sequences should not be split.
	content := strings.Repeat("👨‍👩‍👧‍👦", MaxOutputLength)

	out := TruncateOutput(content)
	require.True(t, utf8.ValidString(out), "truncated output must stay valid UTF-8")
	require.Contains(t, out, "lines truncated")
}

// The schema has to admit the calls the tool's own description asks
// for: a poll carries nothing at all, and keystrokes carry no command.
// A required parameter here rejects those calls before the tool ever
// sees them.
func TestShellTool_SchemaRequiresNothing(t *testing.T) {
	t.Parallel()

	tool := newShellToolForTest(t, t.TempDir())
	require.Empty(t, tool.Info().Required,
		"every bash parameter is optional; a poll is an empty call")

	for _, name := range []string{"command", "reset"} {
		require.Contains(t, tool.Info().Parameters, name)
	}
}

func TestConflictingShellInputs(t *testing.T) {
	t.Parallel()

	require.Empty(t, conflictingShellInputs(ShellParams{Command: "ls"}))
	require.Empty(t, conflictingShellInputs(ShellParams{}), "a poll asks for nothing and is fine")

	conflict := conflictingShellInputs(ShellParams{Command: "ls", Reset: true})
	require.Contains(t, conflict, "command")
	require.Contains(t, conflict, "reset")
}

func TestShellTool_RejectsCombinedCall(t *testing.T) {
	requireTerminalSession(t)
	t.Parallel()

	tool := newShellToolForTest(t, t.TempDir())
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runShellTool(t, tool, ctx, ShellParams{
		Command: "echo hi",
		Reset:   true,
	})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "send one per call")
}

func TestShellLabel(t *testing.T) {
	t.Parallel()

	require.Equal(t, "poll terminal session", shellLabel(ShellParams{}))
	require.Equal(t, "reset terminal session", shellLabel(ShellParams{Reset: true}))
	require.Equal(t, "git status", shellLabel(ShellParams{Command: "git status\ngit log"}),
		"a command with no description labels itself with its first line")
	require.LessOrEqual(t, utf8.RuneCountInString(shellLabel(ShellParams{Command: strings.Repeat("x", 200)})), 60,
		"a runaway command line is shortened to something that fits a label")
}

func TestShellExitNote(t *testing.T) {
	t.Parallel()

	code := 5
	require.Equal(t,
		"[shell had exited (exit code 5); a fresh one replaced it and kept none of its state]",
		shellExitNote(&shellExit{Code: &code}))
	require.Equal(t,
		"[shell had exited (killed by a signal); a fresh one replaced it and kept none of its state]",
		shellExitNote(&shellExit{Reason: "killed by a signal"}))
	require.Equal(t,
		"[shell had exited; a fresh one replaced it and kept none of its state]",
		shellExitNote(&shellExit{}))

	note := shellExitNote(&shellExit{Code: &code, Output: "DYING-LATE"})
	require.Contains(t, note, "Its final output:")
	require.Contains(t, note, "DYING-LATE")
}
