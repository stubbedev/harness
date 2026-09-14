package tools

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
)

func TestShellTool_DefaultAutoBackgroundThreshold(t *testing.T) {
	requireTerminalSession(t)
	workingDir := t.TempDir()
	tool := newShellToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runShellTool(t, tool, ctx, ShellParams{
		Description: "default threshold",
		Command:     "echo done",
	})

	require.False(t, resp.IsError)
	var meta ShellResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.False(t, meta.Background)
	require.Empty(t, meta.ShellID)
	require.Contains(t, meta.Output, "done")
}

func TestShellTool_CustomAutoBackgroundThreshold(t *testing.T) {
	requireTerminalSession(t)
	workingDir := t.TempDir()
	tool := newShellToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	// auto_background_after is the wait budget: when it expires the
	// command stays alive in the persistent terminal session instead
	// of being moved to a background shell.
	resp := runShellTool(t, tool, ctx, ShellParams{
		Description:         "custom threshold",
		Command:             "sleep 1.5 && echo done",
		AutoBackgroundAfter: 1,
	})

	require.False(t, resp.IsError)
	var meta ShellResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.False(t, meta.Background)
	require.Empty(t, meta.ShellID)
	require.NotContains(t, resp.Content, "moved to background")

	// A follow-up call in the same session observes later commands
	// finishing normally.
	resp = runShellTool(t, tool, ctx, ShellParams{
		Description: "follow up",
		Command:     "echo after",
	})
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "after")
}

func newShellToolForTest(workingDir string) fantasy.AgentTool {
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	return NewShellTool(workingDir, "test", attribution, "test-model", nil)
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

	tool := newShellToolForTest(t.TempDir())
	require.Empty(t, tool.Info().Required,
		"every bash parameter is optional; a poll is an empty call")

	for _, name := range []string{"description", "command", "input", "keys", "reset"} {
		require.Contains(t, tool.Info().Parameters, name)
	}
}

func TestConflictingShellInputs(t *testing.T) {
	t.Parallel()

	require.Empty(t, conflictingShellInputs(ShellParams{Command: "ls"}))
	require.Empty(t, conflictingShellInputs(ShellParams{Keys: "ctrl+c"}))
	require.Empty(t, conflictingShellInputs(ShellParams{}), "a poll asks for nothing and is fine")

	conflict := conflictingShellInputs(ShellParams{Command: "ls", Keys: "ctrl+c"})
	require.Contains(t, conflict, "command")
	require.Contains(t, conflict, "keys")

	require.NotEmpty(t, conflictingShellInputs(ShellParams{Command: "ls", Reset: true}))
	require.NotEmpty(t, conflictingShellInputs(ShellParams{Input: "y\n", Reset: true}))
}

func TestShellTool_RejectsCombinedCall(t *testing.T) {
	requireTerminalSession(t)
	t.Parallel()

	tool := newShellToolForTest(t.TempDir())
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runShellTool(t, tool, ctx, ShellParams{
		Description: "two things at once",
		Command:     "echo hi",
		Keys:        "ctrl+c",
	})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "send one per call")
}

func TestShellLabel(t *testing.T) {
	t.Parallel()

	require.Equal(t, "mine", shellLabel(ShellParams{Description: "mine", Command: "ls"}))
	require.Equal(t, "poll terminal session", shellLabel(ShellParams{}))
	require.Equal(t, "keys: ctrl+c", shellLabel(ShellParams{Keys: "ctrl+c"}))
	require.Equal(t, "reset terminal session", shellLabel(ShellParams{Reset: true}))
	require.Equal(t, "input to running program", shellLabel(ShellParams{Input: "y\n"}))
	require.Equal(t, "git status", shellLabel(ShellParams{Command: "git status\ngit log"}),
		"a command with no description labels itself with its first line")
	require.LessOrEqual(t, utf8.RuneCountInString(shellLabel(ShellParams{Command: strings.Repeat("x", 200)})), 60,
		"a runaway command line is shortened to something that fits a label")
}
