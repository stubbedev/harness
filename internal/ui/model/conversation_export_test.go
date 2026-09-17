package model

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/session"
)

func TestConversationExportPath(t *testing.T) {
	t.Parallel()

	require.Equal(
		t,
		filepath.Join(".harness", "exports", "sess-123.md"),
		conversationExportPath(".harness", "sess-123"),
	)
}

func testConversation() (session.Session, []message.Message) {
	sess := session.Session{ID: "sess-123", Title: "Fix the parser"}
	msgs := []message.Message{
		{
			ID:        "msg-1",
			SessionID: "sess-123",
			Role:      message.User,
			CreatedAt: time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC).Unix(),
			Parts:     []message.ContentPart{message.TextContent{Text: "why is the parser slow?"}},
		},
		{
			ID:        "msg-2",
			SessionID: "sess-123",
			Role:      message.Assistant,
			Model:     "claude-opus-5",
			CreatedAt: time.Date(2026, 9, 11, 10, 0, 5, 0, time.UTC).Unix(),
			Parts: []message.ContentPart{
				message.ReasoningContent{Thinking: "check the hot loop"},
				message.TextContent{Text: "Let me look."},
				message.ToolCall{ID: "call-s", Name: "skill_search", Input: `{"query":"testing"}`},
				message.ToolCall{ID: "call-1", Name: "view", Input: `{"file_path":"parser.go"}`},
			},
		},
		{
			ID:        "msg-3",
			SessionID: "sess-123",
			Role:      message.Tool,
			Parts: []message.ContentPart{
				message.ToolResult{ToolCallID: "call-s", Name: "skill_search", Content: "found: pr-builder"},
				message.ToolResult{ToolCallID: "call-1", Name: "view", Content: "package parser"},
			},
		},
	}
	return sess, msgs
}

func TestRenderConversationMarkdown(t *testing.T) {
	t.Parallel()

	exportedAt := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)

	t.Run("renders header, messages and tool results", func(t *testing.T) {
		t.Parallel()
		sess, msgs := testConversation()

		out, err := renderConversationMarkdown(sess, msgs, exportedAt)
		require.NoError(t, err)

		require.Contains(t, out, "# Fix the parser")
		require.Contains(t, out, "- Session: `sess-123`")
		require.Contains(t, out, "- Exported: "+exportedAt.Format(time.RFC3339))
		require.Contains(t, out, "- Messages: 3")
		require.Contains(t, out, "## User")
		require.Contains(t, out, "why is the parser slow?")
		require.Contains(t, out, "## Assistant · claude-opus-5")
		// Model reasoning is scratch space, not part of the visible
		// conversation, so it never appears in the export.
		require.NotContains(t, out, "<details><summary>Thinking</summary>")
		require.NotContains(t, out, "check the hot loop")
		// Context plumbing never shows on screen, so never in the export.
		require.NotContains(t, out, "skill_search")
		require.NotContains(t, out, "found: pr-builder")
		require.Contains(t, out, "#### Tool: view")
		require.Contains(t, out, `"file_path": "parser.go"`)
		require.Contains(t, out, "package parser")
		// Tool-role messages get no section of their own.
		require.NotContains(t, out, "## tool")
	})

	t.Run("falls back to a generic title", func(t *testing.T) {
		t.Parallel()
		sess, msgs := testConversation()
		sess.Title = ""

		out, err := renderConversationMarkdown(sess, msgs, exportedAt)
		require.NoError(t, err)
		require.Contains(t, out, "# Harness Session")
	})

	t.Run("marks tool calls with no result", func(t *testing.T) {
		t.Parallel()
		sess, msgs := testConversation()
		msgs = msgs[:2]

		out, err := renderConversationMarkdown(sess, msgs, exportedAt)
		require.NoError(t, err)
		require.Contains(t, out, "_No result recorded._")
	})

	t.Run("labels errored tool results", func(t *testing.T) {
		t.Parallel()
		sess, msgs := testConversation()
		msgs[2].Parts = []message.ContentPart{
			message.ToolResult{ToolCallID: "call-1", Name: "view", Content: "no such file", IsError: true},
		}

		out, err := renderConversationMarkdown(sess, msgs, exportedAt)
		require.NoError(t, err)
		require.Contains(t, out, "Error:")
		require.Contains(t, out, "no such file")
	})

	t.Run("renders shell commands", func(t *testing.T) {
		t.Parallel()
		sess, _ := testConversation()
		msgs := []message.Message{{
			ID:        "msg-1",
			SessionID: "sess-123",
			Role:      message.User,
			Parts: []message.ContentPart{
				message.ShellCommand{Command: "ls", Output: "go.mod", ExitCode: 1},
			},
		}}

		out, err := renderConversationMarkdown(sess, msgs, exportedAt)
		require.NoError(t, err)
		require.Contains(t, out, "```console\nls\n```")
		require.Contains(t, out, "go.mod")
		require.Contains(t, out, "Exit code: 1")
	})

	t.Run("errors on an empty conversation", func(t *testing.T) {
		t.Parallel()

		_, err := renderConversationMarkdown(session.Session{ID: "sess-123"}, nil, exportedAt)
		require.Error(t, err)
	})
}

func TestCodeBlockEscapesBackticks(t *testing.T) {
	t.Parallel()

	require.Equal(t, "```\nplain\n```", codeBlock("", "plain"))
	require.Equal(t, "````\na ``` fence\n````", codeBlock("", "a ``` fence"))
}

func TestPrettyJSONLeavesInvalidInputAlone(t *testing.T) {
	t.Parallel()

	require.Equal(t, "{\n  \"a\": 1\n}", prettyJSON(`{"a":1}`))
	require.Equal(t, `{"a":`, prettyJSON(`{"a":`))
}

func TestSaveConversationExport(t *testing.T) {
	t.Parallel()

	sess, msgs := testConversation()

	t.Run("writes the transcript to disk", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		path, content, err := saveConversationExport(dir, sess, msgs)
		require.NoError(t, err)
		require.Equal(t, conversationExportPath(dir, "sess-123"), path)

		b, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Contains(t, string(b), "why is the parser slow?")
		require.Equal(t, string(b), content)
	})

	t.Run("overwrites the previous export", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		path, _, err := saveConversationExport(dir, sess, msgs)
		require.NoError(t, err)

		grown := append(msgs, message.Message{
			ID:        "msg-4",
			SessionID: "sess-123",
			Role:      message.Assistant,
			Parts:     []message.ContentPart{message.TextContent{Text: "found it"}},
		})
		path2, _, err := saveConversationExport(dir, sess, grown)
		require.NoError(t, err)
		require.Equal(t, path, path2)

		b, err := os.ReadFile(path2)
		require.NoError(t, err)
		require.Contains(t, string(b), "found it")
	})

	t.Run("errors when there is nothing to export", func(t *testing.T) {
		t.Parallel()

		_, _, err := saveConversationExport(t.TempDir(), sess, nil)
		require.Error(t, err)
	})
}
