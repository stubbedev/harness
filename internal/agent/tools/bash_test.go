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

func TestBashTool_DefaultAutoBackgroundThreshold(t *testing.T) {
	requireTerminalSession(t)
	workingDir := t.TempDir()
	tool := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "default threshold",
		Command:     "echo done",
	})

	require.False(t, resp.IsError)
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.False(t, meta.Background)
	require.Empty(t, meta.ShellID)
	require.Contains(t, meta.Output, "done")
}

func TestBashTool_CustomAutoBackgroundThreshold(t *testing.T) {
	requireTerminalSession(t)
	workingDir := t.TempDir()
	tool := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	// auto_background_after is the wait budget: when it expires the
	// command stays alive in the persistent terminal session instead
	// of being moved to a background shell.
	resp := runBashTool(t, tool, ctx, BashParams{
		Description:         "custom threshold",
		Command:             "sleep 1.5 && echo done",
		AutoBackgroundAfter: 1,
	})

	require.False(t, resp.IsError)
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.False(t, meta.Background)
	require.Empty(t, meta.ShellID)
	require.NotContains(t, resp.Content, "moved to background")

	// A follow-up call in the same session observes later commands
	// finishing normally.
	resp = runBashTool(t, tool, ctx, BashParams{
		Description: "follow up",
		Command:     "echo after",
	})
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "after")
}

func newBashToolForTest(workingDir string) fantasy.AgentTool {
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	return NewBashTool(workingDir, attribution, "test-model", nil)
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

func runBashTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params BashParams) fantasy.ToolResponse {
	t.Helper()

	input, err := json.Marshal(params)
	require.NoError(t, err)

	call := fantasy.ToolCall{
		ID:    "test-call",
		Name:  BashToolName,
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
