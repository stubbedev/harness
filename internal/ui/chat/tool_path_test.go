package chat

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
)

// TestToolHeaderShowsWorkspaceRelativePath pins the label a workspace
// buys: the same file the agent touched, without the absolute prefix
// that made every row long enough to truncate.
func TestToolHeaderShowsWorkspaceRelativePath(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	base := t.TempDir()
	file := filepath.Join(base, "internal", "ui", "chat", "toolgroup.go")
	input, err := json.Marshal(map[string]string{"file_path": file})
	require.NoError(t, err)

	item := NewToolMessageItem(sty, "msg", message.ToolCall{
		ID: "t1", Name: "view", Input: string(input), Finished: true,
	}, &message.ToolResult{Content: "package chat"}, false, &ItemEnv{WorkingDir: base})

	header, _, _ := strings.Cut(ansi.Strip(item.RawRender(120)), "\n")
	assert.Contains(t, header, filepath.Join("internal", "ui", "chat", "toolgroup.go"))
	assert.NotContains(t, header, base, "the workspace prefix must not be shown")
}

// TestToolHeaderWithoutWorkspaceStaysAbsolute pins the fallback: an item
// built without an env keeps the path it was given (home-shortened only),
// so restored transcripts render exactly as before.
func TestToolHeaderWithoutWorkspaceStaysAbsolute(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	file := filepath.Join(t.TempDir(), "internal", "main.go")
	input, err := json.Marshal(map[string]string{"file_path": file})
	require.NoError(t, err)

	item := NewToolMessageItem(sty, "msg", message.ToolCall{
		ID: "t1", Name: "view", Input: string(input), Finished: true,
	}, &message.ToolResult{Content: "package main"}, false)

	header, _, _ := strings.Cut(ansi.Strip(item.RawRender(120)), "\n")
	assert.Contains(t, header, file)
}

// TestToolCallSummaryUsesWorkspacePath pins that the collapsed group's
// one-liner labels a file exactly like the call's own header does.
func TestToolCallSummaryUsesWorkspacePath(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	file := filepath.Join(base, "cmd", "main.go")
	input, err := json.Marshal(map[string]string{"file_path": file})
	require.NoError(t, err)

	got := ToolCallSummary(message.ToolCall{Name: "view", Input: string(input)}, base)
	assert.Equal(t, filepath.Join("cmd", "main.go"), got)
}
