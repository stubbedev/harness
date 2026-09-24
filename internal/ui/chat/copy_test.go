package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/message"
)

// copyItem builds a finished tool item and hands back its copy text.
// Paths in these tests sit under /srv so fsext.PrettyPath, which
// shortens the home directory and nothing else, leaves them alone.
func copyItem(t *testing.T, name string, input any, result *message.ToolResult) string {
	t.Helper()
	raw, err := json.Marshal(input)
	require.NoError(t, err)
	if result != nil {
		result.ToolCallID = "call-1"
		result.Name = name
	}
	item := NewToolMessageItem(groupStyles(), "msg", message.ToolCall{
		ID: "call-1", Name: name, Input: string(raw), Finished: true,
	}, result, false)
	c, ok := item.(interface{ formatToolForCopy() string })
	require.True(t, ok, "%s item does not implement the copy hook", name)
	return c.formatToolForCopy()
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}

func TestToolCopy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		tool   string
		input  any
		result *message.ToolResult
		want   string
	}{
		{
			name:  "shell fences the command and the output with a duration",
			tool:  tools.ShellToolName,
			input: tools.ShellParams{Command: "go test ./...\ngo vet ./..."},
			result: &message.ToolResult{
				Content: "ok",
				Metadata: mustJSON(t, tools.ShellResponseMetadata{
					Output: "ok\n", StartTime: 1_000, EndTime: 1_250,
				}),
			},
			want: "## Shell Tool Call\n\n" +
				"### Parameters:\n\n" +
				"**Command:**\n\n```bash\ngo test ./...\ngo vet ./...\n```\n\n" +
				"### Result:\n\n" +
				"**Duration:** 250ms\n\n```bash\nok\n```",
		},
		{
			name:   "shell with no output says so rather than emitting an empty fence",
			tool:   tools.ShellToolName,
			input:  tools.ShellParams{Command: "true"},
			result: &message.ToolResult{Content: tools.ShellNoOutput},
			want: "## Shell Tool Call\n\n" +
				"### Parameters:\n\n" +
				"**Command:**\n\n```bash\ntrue\n```\n\n" +
				"### Result:\n\n(no output)",
		},
		{
			name:  "view tags the fence from the file extension",
			tool:  tools.ViewToolName,
			input: tools.ViewParams{FilePath: "/srv/app/main.go", Offset: 10, Limit: 5},
			result: &message.ToolResult{
				Content: "unused",
				Metadata: mustJSON(t, tools.ViewResponseMetadata{
					FilePath: "/srv/app/main.go", Content: "package main",
				}),
			},
			want: "## View Tool Call\n\n" +
				"### Parameters:\n\n" +
				"**File:** /srv/app/main.go\n**Offset:** 10\n**Limit:** 5\n\n" +
				"### Result:\n\n```go\npackage main\n```",
		},
		{
			name:  "view of an extension outside the old fourteen still gets a language",
			tool:  tools.ViewToolName,
			input: tools.ViewParams{FilePath: "/srv/app/main.zig"},
			result: &message.ToolResult{
				Content: "unused",
				Metadata: mustJSON(t, tools.ViewResponseMetadata{
					FilePath: "/srv/app/main.zig", Content: "const x = 1;",
				}),
			},
			want: "## View Tool Call\n\n" +
				"### Parameters:\n\n" +
				"**File:** /srv/app/main.zig\n\n" +
				"### Result:\n\n```zig\nconst x = 1;\n```",
		},
		{
			name: "edit reports the diff, not the replaced strings",
			tool: tools.EditToolName,
			input: tools.EditParams{
				FilePath: "/srv/app/a.txt",
				Edits:    []tools.EditOperation{{OldString: "one", NewString: "two"}},
			},
			result: &message.ToolResult{
				Content: "edited",
				Metadata: mustJSON(t, tools.EditResponseMetadata{
					OldContent: "one\n", NewContent: "two\n",
				}),
			},
			want: "## Edit Tool Call\n\n" +
				"### Parameters:\n\n" +
				"**File:** /srv/app/a.txt\n**Edits:** 1\n\n" +
				"### Result:\n\nChanges: +1 -1",
		},
		{
			name:   "write reproduces the written file from the input",
			tool:   tools.WriteToolName,
			input:  tools.WriteParams{FilePath: "/srv/app/x.py", Content: "print(1)"},
			result: &message.ToolResult{Content: "File successfully written"},
			want: "## Write Tool Call\n\n" +
				"### Parameters:\n\n" +
				"**File:** /srv/app/x.py\n\n" +
				"### Result:\n\n```python\nprint(1)\n```",
		},
		{
			name:   "fetch fences the document in the requested format",
			tool:   tools.FetchToolName,
			input:  tools.FetchParams{URL: "https://example.com", Format: "markdown", Timeout: 30},
			result: &message.ToolResult{Content: "# Title"},
			want: "## Fetch Tool Call\n\n" +
				"### Parameters:\n\n" +
				"**URL:** https://example.com\n**Format:** markdown\n**Timeout:** 30s\n\n" +
				"### Result:\n\n```markdown\n# Title\n```",
		},
		{
			name:   "an unknown tool's parameters are sorted, indented JSON",
			tool:   "zebra_tool",
			input:  map[string]any{"zeta": 1, "alpha": "a", "middle": true},
			result: &message.ToolResult{Content: "done"},
			want: "## Zebra Tool Tool Call\n\n" +
				"### Parameters:\n\n" +
				"```json\n{\n  \"alpha\": \"a\",\n  \"middle\": true,\n  \"zeta\": 1\n}\n```\n\n" +
				"### Result:\n\n```\ndone\n```",
		},
		{
			name:   "an MCP call names its server and tool and fences its JSON result",
			tool:   "mcp_files_read_file",
			input:  map[string]any{"path": "/srv/a"},
			result: &message.ToolResult{Content: `{"b":2,"a":1}`},
			want: "## Files -> Read File Tool Call\n\n" +
				"### Parameters:\n\n" +
				"```json\n{\n  \"path\": \"/srv/a\"\n}\n```\n\n" +
				"### Result:\n\n```json\n{\n  \"a\": 1,\n  \"b\": 2\n}\n```",
		},
		{
			name:   "an error result is fenced like a success",
			tool:   tools.ShellToolName,
			input:  tools.ShellParams{Command: "false"},
			result: &message.ToolResult{Content: "exit status 1", IsError: true},
			want: "## Shell Tool Call\n\n" +
				"### Parameters:\n\n" +
				"**Command:**\n\n```bash\nfalse\n```\n\n" +
				"### Error:\n\n```\nexit status 1\n```",
		},
		{
			name:   "an agent call carries the task and the final report",
			tool:   agent.AgentToolName,
			input:  agent.AgentParams{SubagentType: "explore", Prompt: "Find the parser"},
			result: &message.ToolResult{Content: "It is in parser.go"},
			want: "## Agent Tool Call\n\n" +
				"### Parameters:\n\n" +
				"**Subagent:** explore\n**Task:**\nFind the parser\n\n" +
				"### Result:\n\n```markdown\nIt is in parser.go\n```",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := copyItem(t, tt.tool, tt.input, tt.result)
			if tt.tool == tools.EditToolName {
				// The diff body itself is the diff package's business;
				// pin the header and that a diff fence follows.
				assert.True(t, strings.HasPrefix(got, tt.want), "got:\n%s", got)
				assert.Contains(t, got, "```diff\n")
				return
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestToolCopyPendingAndCancelled(t *testing.T) {
	t.Parallel()

	t.Run("pending", func(t *testing.T) {
		t.Parallel()
		got := copyItem(t, tools.ShellToolName, tools.ShellParams{Command: "sleep 1"}, nil)
		assert.Equal(t, "## Shell Tool Call\n\n"+
			"### Parameters:\n\n**Command:**\n\n```bash\nsleep 1\n```\n\n"+
			"### Status:\n\nPending...", got)
	})

	t.Run("cancelled", func(t *testing.T) {
		t.Parallel()
		item := NewToolMessageItem(groupStyles(), "msg", message.ToolCall{
			ID: "c1", Name: tools.ShellToolName, Input: `{"command":"sleep 1"}`, Finished: true,
		}, nil, true)
		c := item.(interface{ formatToolForCopy() string })
		assert.Contains(t, c.formatToolForCopy(), "### Status:\n\nCancelled")
	})
}

func TestCopyFenceEscapesNestedFences(t *testing.T) {
	t.Parallel()

	// A viewed Markdown file that itself contains a fence must not end
	// the copied block early.
	got := copyItem(t, tools.ViewToolName,
		tools.ViewParams{FilePath: "/srv/README.md"},
		&message.ToolResult{
			Content: "unused",
			Metadata: mustJSON(t, tools.ViewResponseMetadata{
				FilePath: "/srv/README.md",
				Content:  "Run this:\n```sh\nls\n```",
			}),
		})
	assert.Contains(t, got, "````md\nRun this:\n```sh\nls\n```\n````")
}

func TestCopyTruncatesOversizedContent(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("x123456789\n", copyContentLimit/10)
	got := copyItem(t, tools.ViewToolName,
		tools.ViewParams{FilePath: "/srv/big.txt"},
		&message.ToolResult{
			Content:  "unused",
			Metadata: mustJSON(t, tools.ViewResponseMetadata{FilePath: "/srv/big.txt", Content: big}),
		})
	assert.Less(t, len(got), len(big))
	assert.Contains(t, got, "... truncated, ")
	// The cut lands on a line boundary, so no partial line survives.
	assert.NotContains(t, got, "x12345678\n...")
}

func TestGroupCopy(t *testing.T) {
	t.Parallel()

	sty := groupStyles()
	done := func(id, cmd string) ToolMessageItem {
		item := bashTool(id, cmd, true)
		item.SetResult(&message.ToolResult{ToolCallID: id, Name: "shell", Content: "ok"})
		return item
	}

	t.Run("the group copies under a header with the children demoted", func(t *testing.T) {
		t.Parallel()
		g := NewToolGroupMessageItem(sty, done("t1", "ls"))
		g.AddTool(done("t2", "pwd"))

		got := g.formatGroupForCopy()
		assert.True(t, strings.HasPrefix(got, "## Ran (2 tool calls)\n\n"), "got:\n%s", got)
		assert.Equal(t, 2, strings.Count(got, "\n### Shell Tool Call"))
		assert.Equal(t, 0, strings.Count(got, "\n## Shell Tool Call"))
		assert.Equal(t, 2, strings.Count(got, "#### Parameters:"))
		assert.Contains(t, got, "ls")
		assert.Contains(t, got, "pwd")
	})

	t.Run("the sub-cursor narrows copy to its child", func(t *testing.T) {
		t.Parallel()
		g := NewToolGroupMessageItem(sty, done("t1", "ls"))
		g.AddTool(done("t2", "pwd"))
		g.SetFocused(true)
		require.True(t, g.ToggleExpanded())
		require.True(t, g.SelectChildNext())
		require.Equal(t, 0, g.SelectedChild())

		got := g.formatGroupForCopy()
		assert.True(t, strings.HasPrefix(got, "## Shell Tool Call"), "got:\n%s", got)
		assert.Contains(t, got, "ls")
		assert.NotContains(t, got, "pwd")
		assert.NotContains(t, got, "Ran (")
	})

	t.Run("expansion does not change what is copied", func(t *testing.T) {
		t.Parallel()
		g := NewToolGroupMessageItem(sty, done("t1", "ls"))
		g.AddTool(done("t2", "pwd"))
		collapsed := g.formatGroupForCopy()

		g.SetFocused(true)
		require.True(t, g.ToggleExpanded())
		require.Equal(t, -1, g.SelectedChild())
		assert.Equal(t, collapsed, g.formatGroupForCopy())
	})
}

func TestMessageCopy(t *testing.T) {
	t.Parallel()

	t.Run("every text part is copied, thinking is not", func(t *testing.T) {
		t.Parallel()
		msg := &message.Message{Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: "hmm"},
			message.TextContent{Text: "First.\n"},
			message.ToolCall{ID: "t1", Name: "shell"},
			message.TextContent{Text: "Second."},
		}}
		assert.Equal(t, "First.\n\nSecond.", copyMessageText(msg))
	})

	t.Run("attachments are named rather than dropped", func(t *testing.T) {
		t.Parallel()
		msg := &message.Message{Parts: []message.ContentPart{
			message.TextContent{Text: "Look at this"},
			message.BinaryContent{Path: "shot.png", MIMEType: "image/png"},
		}}
		got := copyJoin(copyMessageText(msg), copyAttachments(msg))
		assert.Equal(t, "Look at this\n\n**Attachments:**\n- shot.png (image/png)", got)
	})

	t.Run("a message with no attachments carries no attachment section", func(t *testing.T) {
		t.Parallel()
		msg := &message.Message{Parts: []message.ContentPart{message.TextContent{Text: "Hi"}}}
		assert.Equal(t, "Hi", copyJoin(copyMessageText(msg), copyAttachments(msg)))
	})
}

func TestShellItemCopy(t *testing.T) {
	t.Parallel()

	ok := NewShellItem(groupStyles(), "echo hi", "hi\n", 0).(*ShellItem)
	assert.Equal(t, "```console\n$ echo hi\nhi\n```", ok.copyText())

	// A failing run carries the exit code the transcript shows.
	failed := NewShellItem(groupStyles(), "false", "", 1).(*ShellItem)
	assert.Equal(t, "```console\n$ false (exit 1)\n```", failed.copyText())
}
