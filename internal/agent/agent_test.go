package agent

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/message"
)

func TestMain(m *testing.M) {
	slog.SetLogLoggerLevel(slog.LevelError)
	// The persistent terminal session runs a plain sh: deterministic
	// startup (no rc files) keeps the shell tool results stable.
	os.Setenv("SHELL", "/bin/sh")
	os.Exit(m.Run())
}

// scriptedAgent builds a coder agent whose large model replays the given
// script. The small model (titles, summaries) always answers in words.
func scriptedAgent(t *testing.T, client *http.Client, turns ...scriptedTurn) (SessionAgent, fakeEnv, *scriptedModel) {
	t.Helper()

	env := testEnv(t)
	createSimpleGoProject(t, env.workingDir)

	large := newScriptedModel(turns...)
	agent, err := coderAgent(client, env, large, textModel("A Session"))
	require.NoError(t, err)
	return agent, env, large
}

// runScript drives one agent turn to completion and returns the messages
// it persisted.
func runScript(t *testing.T, agent SessionAgent, env fakeEnv, prompt string) []message.Message {
	t.Helper()

	sess, err := env.sessions.Create(t.Context(), "New Session")
	require.NoError(t, err)

	res, err := agent.Run(t.Context(), SessionAgentCall{
		Prompt:          prompt,
		SessionID:       sess.ID,
		MaxOutputTokens: 10000,
	})
	require.NoError(t, err)
	require.NotNil(t, res)

	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	// The runtime block and other harness context are stored as
	// ContextNote rows so they stay in the cached prefix; they are not
	// conversation and the scripts here assert on the conversation.
	return slices.DeleteFunc(msgs, func(m message.Message) bool { return m.ContextNotesOnly() })
}

// toolResults pairs every tool result back to the call that produced it
// and returns them keyed by tool name. Pairing by call id (rather than
// just scanning for a tool name) is the part of the agent loop these
// tests exist to check: a result that reaches the conversation under the
// wrong id is indistinguishable from a correct one by name alone.
func toolResults(t *testing.T, msgs []message.Message) map[string]message.ToolResult {
	t.Helper()

	nameByCallID := map[string]string{}
	for _, msg := range msgs {
		if msg.Role != message.Assistant {
			continue
		}
		for _, tc := range msg.ToolCalls() {
			require.NotEmpty(t, tc.ID, "tool call %q has no id", tc.Name)
			require.NotContains(t, nameByCallID, tc.ID, "duplicate tool call id %q", tc.ID)
			nameByCallID[tc.ID] = tc.Name
		}
	}

	out := map[string]message.ToolResult{}
	for _, msg := range msgs {
		if msg.Role != message.Tool {
			continue
		}
		for _, tr := range msg.ToolResults() {
			name, ok := nameByCallID[tr.ToolCallID]
			require.True(t, ok, "tool result for unknown call id %q", tr.ToolCallID)
			out[name] = tr
		}
	}
	return out
}

// serveOnce returns an http client that answers every request with body,
// along with the URL to point a tool at. It replaces the recorded HTTP
// traffic the network-facing tools used to replay from a cassette.
func serveOnce(t *testing.T, contentType, body string) (*http.Client, string) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.Client(), srv.URL
}

// TestCoderAgent checks the agent loop against a scripted model: a tool
// call the model asks for is dispatched, its result is persisted and
// paired back to the call, and its side effects land on disk.
//
// The model's decisions are scripted rather than replayed from a recorded
// provider. See scriptedModel for why.
func TestCoderAgent(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows for now")
	}

	t.Run("plain answer persists both messages", func(t *testing.T) {
		t.Parallel()

		agent, env, _ := scriptedAgent(t, nil, scriptedTurn{text: "Hello back"})
		msgs := runScript(t, agent, env, "Hello")

		require.Len(t, msgs, 2)
		require.Equal(t, message.User, msgs[0].Role)
		require.Equal(t, message.Assistant, msgs[1].Role)
		require.Contains(t, msgs[1].Content().Text, "Hello back")
	})

	t.Run("view", func(t *testing.T) {
		t.Parallel()

		agent, env, _ := scriptedAgent(t, nil, scriptedTurn{
			calls: []scriptedCall{{
				name:  tools.ViewToolName,
				input: map[string]any{"file_path": "go.mod"},
			}},
		})
		res := toolResults(t, runScript(t, agent, env, "Read the go mod"))

		view, ok := res[tools.ViewToolName]
		require.True(t, ok, "expected a view result")
		require.False(t, view.IsError, "view failed: %s", view.Content)
		require.Contains(t, view.Content, "module example.com/testproject")
	})

	t.Run("edit rewrites the file", func(t *testing.T) {
		t.Parallel()

		agent, env, _ := scriptedAgent(t, nil,
			scriptedTurn{calls: []scriptedCall{{
				name:  tools.ViewToolName,
				input: map[string]any{"file_path": "main.go"},
			}}},
			scriptedTurn{calls: []scriptedCall{{
				name: tools.EditToolName,
				input: map[string]any{
					"file_path": "main.go",
					"edits": []map[string]any{{
						"old_string": `fmt.Println("Hello, World!")`,
						"new_string": `fmt.Println("hello from harness")`,
					}},
				},
			}}},
		)
		res := toolResults(t, runScript(t, agent, env, "update main.go"))

		require.Contains(t, res, tools.ViewToolName, "expected a read before the write")
		edit, ok := res[tools.EditToolName]
		require.True(t, ok, "expected an edit result")
		require.False(t, edit.IsError, "edit failed: %s", edit.Content)

		content, err := os.ReadFile(filepath.Join(env.workingDir, "main.go"))
		require.NoError(t, err)
		require.Contains(t, strings.ToLower(string(content)), "hello from harness")
	})

	t.Run("write creates the file", func(t *testing.T) {
		t.Parallel()

		agent, env, _ := scriptedAgent(t, nil, scriptedTurn{
			calls: []scriptedCall{{
				name: tools.WriteToolName,
				input: map[string]any{
					"file_path": "greeting.txt",
					"content":   "hello from write",
				},
			}},
		})
		res := toolResults(t, runScript(t, agent, env, "write a greeting"))

		write, ok := res[tools.WriteToolName]
		require.True(t, ok, "expected a write result")
		require.False(t, write.IsError, "write failed: %s", write.Content)

		content, err := os.ReadFile(filepath.Join(env.workingDir, "greeting.txt"))
		require.NoError(t, err)
		require.Equal(t, "hello from write", string(content))
	})

	t.Run("multiedit applies every edit", func(t *testing.T) {
		t.Parallel()

		agent, env, _ := scriptedAgent(t, nil,
			scriptedTurn{calls: []scriptedCall{{
				name:  tools.ViewToolName,
				input: map[string]any{"file_path": "main.go"},
			}}},
			scriptedTurn{calls: []scriptedCall{{
				name: tools.EditToolName,
				input: map[string]any{
					"file_path": "main.go",
					"edits": []any{
						map[string]any{
							"old_string": "Hello, World!",
							"new_string": "Hello, Harness!",
						},
						map[string]any{
							"old_string": "\tfmt.Println",
							"new_string": "\t// Greeting\n\tfmt.Println",
						},
					},
				},
			}}},
		)
		res := toolResults(t, runScript(t, agent, env, "multiedit main.go"))

		multi, ok := res[tools.EditToolName]
		require.True(t, ok, "expected a multiedit result")
		require.False(t, multi.IsError, "multiedit failed: %s", multi.Content)

		content, err := os.ReadFile(filepath.Join(env.workingDir, "main.go"))
		require.NoError(t, err)
		require.Contains(t, string(content), "Hello, Harness!")
		require.Contains(t, string(content), "// Greeting")
	})

	t.Run("shell", func(t *testing.T) {
		t.Parallel()

		agent, env, _ := scriptedAgent(t, nil, scriptedTurn{
			calls: []scriptedCall{{
				name: tools.ShellToolName,
				input: map[string]any{
					"command":     "printf 'hello shell' > test.txt",
					"description": "create test.txt",
				},
			}},
		})
		res := toolResults(t, runScript(t, agent, env, "create test.txt"))

		sh, ok := res[tools.ShellToolName]
		require.True(t, ok, "expected a shell result")
		require.False(t, sh.IsError, "shell failed: %s", sh.Content)

		content, err := os.ReadFile(filepath.Join(env.workingDir, "test.txt"))
		require.NoError(t, err)
		require.Contains(t, string(content), "hello shell")
	})

	// Downloading is a fetch parameter, and the file lands in the
	// session's scratch directory rather than the working tree.
	t.Run("fetch with download writes the body to disk", func(t *testing.T) {
		t.Parallel()

		client, addr := serveOnce(t, "text/plain", "downloaded body")
		agent, env, _ := scriptedAgent(t, client, scriptedTurn{
			calls: []scriptedCall{{
				name: tools.FetchToolName,
				input: map[string]any{
					"url":      addr + "/example.txt",
					"download": true,
				},
			}},
		})
		res := toolResults(t, runScript(t, agent, env, "download the file"))

		dl, ok := res[tools.FetchToolName]
		require.True(t, ok, "expected a fetch result")
		require.False(t, dl.IsError, "download failed: %s", dl.Content)

		path := downloadedPath(t, dl.Content)
		t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(path)) })
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, "downloaded body", string(content))
		require.Equal(t, "example.txt", filepath.Base(path))
	})

	t.Run("fetch returns the body", func(t *testing.T) {
		t.Parallel()

		client, addr := serveOnce(t, "text/html", "<html><body><p>John Doe</p></body></html>")
		agent, env, _ := scriptedAgent(t, client, scriptedTurn{
			calls: []scriptedCall{{
				name: tools.FetchToolName,
				input: map[string]any{
					"url":    addr + "/example.html",
					"format": "text",
				},
			}},
		})
		res := toolResults(t, runScript(t, agent, env, "fetch the page"))

		fetch, ok := res[tools.FetchToolName]
		require.True(t, ok, "expected a fetch result")
		require.False(t, fetch.IsError, "fetch failed: %s", fetch.Content)
		require.Contains(t, fetch.Content, "John Doe")
	})

	// Two calls in one assistant message must both be dispatched and both
	// come back paired to their own call.
	t.Run("parallel tool calls", func(t *testing.T) {
		t.Parallel()

		agent, env, _ := scriptedAgent(t, nil, scriptedTurn{
			calls: []scriptedCall{
				{name: tools.ShellToolName, input: map[string]any{"command": "ls *.go"}},
				{name: tools.ViewToolName, input: map[string]any{"file_path": "main.go"}},
			},
		})
		msgs := runScript(t, agent, env, "list and read at once")

		var withCalls *message.Message
		for i, msg := range msgs {
			if msg.Role == message.Assistant && len(msg.ToolCalls()) > 0 {
				withCalls = &msgs[i]
			}
		}
		require.NotNil(t, withCalls, "expected an assistant message carrying tool calls")
		require.Len(t, withCalls.ToolCalls(), 2, "both calls belong to one message")

		res := toolResults(t, msgs)
		sh, ok := res[tools.ShellToolName]
		require.True(t, ok, "expected a shell result")
		require.False(t, sh.IsError, "shell failed: %s", sh.Content)
		require.Contains(t, sh.Content, "main.go")

		view, ok := res[tools.ViewToolName]
		require.True(t, ok, "expected a view result")
		require.False(t, view.IsError, "view failed: %s", view.Content)
	})

	// A failing tool must come back as an error result the model can see,
	// not as a hard error that aborts the turn.
	t.Run("tool error reaches the conversation", func(t *testing.T) {
		t.Parallel()

		agent, env, _ := scriptedAgent(t, nil, scriptedTurn{
			calls: []scriptedCall{{
				name:  tools.ViewToolName,
				input: map[string]any{"file_path": "does-not-exist.go"},
			}},
		})
		res := toolResults(t, runScript(t, agent, env, "read a missing file"))

		view, ok := res[tools.ViewToolName]
		require.True(t, ok, "expected a view result even though it failed")
		require.True(t, view.IsError, "expected an error result, got: %s", view.Content)
	})

	// The loop must keep going after a tool result: the model gets another
	// turn, and what it says then is what the conversation ends on.
	t.Run("loop continues after a tool result", func(t *testing.T) {
		t.Parallel()

		agent, env, model := scriptedAgent(t, nil,
			scriptedTurn{calls: []scriptedCall{{
				name:  tools.ShellToolName,
				input: map[string]any{"command": "ls"},
			}}},
			scriptedTurn{text: "I looked, and there are two files."},
		)
		msgs := runScript(t, agent, env, "what is in here")

		require.GreaterOrEqual(t, len(model.sentCalls()), 2,
			"the model must be called again after the tool result")
		require.Contains(t, msgs[len(msgs)-1].Content().Text, "there are two files")
	})
}

func BenchmarkBuildSummaryPrompt(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = buildSummaryPrompt("keep the auth work")
	}
}

func TestPreparePrompt_FiltersImageAttachments(t *testing.T) {
	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "test prompt")
	agent := sa.(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "test")
	require.NoError(t, err)

	// User message with text, a text attachment, and an image attachment.
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: "hello world"},
			message.BinaryContent{Path: "notes.txt", MIMEType: "text/plain", Data: []byte("important notes")},
			message.BinaryContent{Path: "image.png", MIMEType: "image/png", Data: []byte("fake-image-data")},
		},
	})
	require.NoError(t, err)

	msgs, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	// New-turn image attachment (not yet stored in the DB).
	imageAtt := message.Attachment{
		FileName: "screenshot.png",
		MimeType: "image/png",
		Content:  []byte("fake-screenshot"),
	}

	// When supportsImages is false, image attachments should be stripped
	// from history AND from the files list.
	history, files := agent.preparePrompt(msgs, false, imageAtt)
	require.Len(t, history, 1)
	require.Len(t, history[0].Content, 1)
	text, ok := fantasy.AsMessagePart[fantasy.TextPart](history[0].Content[0])
	require.True(t, ok)
	require.Contains(t, text.Text, "hello world")
	require.Contains(t, text.Text, "important notes")
	require.Empty(t, files, "image files should be excluded when model does not support images")

	// When supportsImages is true, image attachments should remain in
	// history and be included in the files list.
	history, files = agent.preparePrompt(msgs, true, imageAtt)
	require.Len(t, history, 1)
	require.Len(t, history[0].Content, 2)
	text, ok = fantasy.AsMessagePart[fantasy.TextPart](history[0].Content[0])
	require.True(t, ok)
	require.Contains(t, text.Text, "hello world")
	file, ok := fantasy.AsMessagePart[fantasy.FilePart](history[0].Content[1])
	require.True(t, ok)
	require.Equal(t, "image.png", file.Filename)
	require.Len(t, files, 1, "new-turn image attachment should be included when model supports images")
	require.Equal(t, "screenshot.png", files[0].Filename)
}

func TestPreparePrompt_WhitespaceOnlyAssistantDropped(t *testing.T) {
	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "test prompt")
	agent := sa.(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "test")
	require.NoError(t, err)

	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: "hello"},
		},
	})
	require.NoError(t, err)

	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.TextContent{Text: "\n"},
			message.Finish{Reason: message.FinishReasonEndTurn},
		},
	})
	require.NoError(t, err)

	msgs, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	history, _ := agent.preparePrompt(msgs, true)
	for _, msg := range history {
		require.NotEqual(t, fantasy.MessageRoleAssistant, msg.Role, "whitespace-only assistant must be omitted")
	}
	foundUser := false
	for _, msg := range history {
		if msg.Role == fantasy.MessageRoleUser {
			for _, part := range msg.Content {
				if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok && strings.Contains(text.Text, "hello") {
					foundUser = true
				}
			}
		}
	}
	require.True(t, foundUser, "user hello must remain")

	okSess, err := env.sessions.Create(ctx, "ok")
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, okSess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "hello"}},
	})
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, okSess.ID, message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.TextContent{Text: "ok"},
			message.Finish{Reason: message.FinishReasonEndTurn},
		},
	})
	require.NoError(t, err)
	okMsgs, err := env.messages.List(ctx, okSess.ID)
	require.NoError(t, err)
	okHistory, _ := agent.preparePrompt(okMsgs, true)
	foundOK := false
	for _, msg := range okHistory {
		if msg.Role != fantasy.MessageRoleAssistant {
			continue
		}
		for _, part := range msg.Content {
			if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok && text.Text == "ok" {
				foundOK = true
			}
		}
	}
	require.True(t, foundOK, "non-empty assistant text must remain")

	reasonSess, err := env.sessions.Create(ctx, "reason")
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, reasonSess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "hello"}},
	})
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, reasonSess.ID, message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: "plan"},
			message.Finish{Reason: message.FinishReasonEndTurn},
		},
	})
	require.NoError(t, err)
	reasonMsgs, err := env.messages.List(ctx, reasonSess.ID)
	require.NoError(t, err)
	reasonHistory, _ := agent.preparePrompt(reasonMsgs, true)
	foundReason := false
	for _, msg := range reasonHistory {
		if msg.Role == fantasy.MessageRoleAssistant {
			foundReason = true
		}
	}
	require.True(t, foundReason, "reasoning-only assistant must remain")
}

func TestCreateUserMessage_RetainsAllAttachments(t *testing.T) {
	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "test prompt")
	agent := sa.(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "test")
	require.NoError(t, err)

	// Mix of text and image attachments — all should be stored.
	call := SessionAgentCall{
		SessionID: sess.ID,
		Prompt:    "look at this image",
		Attachments: []message.Attachment{
			{FileName: "notes.txt", FilePath: "notes.txt", MimeType: "text/plain", Content: []byte("notes")},
			{FileName: "photo.png", FilePath: "photo.png", MimeType: "image/png", Content: []byte("fake-png")},
		},
	}

	msg, err := agent.createUserMessage(ctx, call)
	require.NoError(t, err)

	// All attachments should be present as BinaryContent parts.
	binaryParts := msg.BinaryContent()
	require.Len(t, binaryParts, 2, "both text and image attachments should be stored in the user message")
	require.Equal(t, "notes.txt", binaryParts[0].Path)
	require.Equal(t, "text/plain", binaryParts[0].MIMEType)
	require.Equal(t, "photo.png", binaryParts[1].Path)
	require.Equal(t, "image/png", binaryParts[1].MIMEType)

	// Reload from DB to verify persistence.
	reloaded, err := env.messages.Get(ctx, msg.ID)
	require.NoError(t, err)
	binaryParts = reloaded.BinaryContent()
	require.Len(t, binaryParts, 2, "attachments should survive DB round-trip")
	require.Equal(t, "photo.png", binaryParts[1].Path)
}

func TestPreparePrompt_OrphanedToolUse(t *testing.T) {
	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "test prompt")
	agent := sa.(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "test")
	require.NoError(t, err)

	// Create a user message.
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: "hello"},
		},
	})
	require.NoError(t, err)

	// Create an assistant message with a tool call but no tool result —
	// this simulates a cancelled/interrupted agent tool call.
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.TextContent{Text: "let me check"},
			message.ToolCall{
				ID:       "call_orphaned_1",
				Name:     "agent",
				Input:    `{"prompt":"do something"}`,
				Finished: true,
			},
		},
	})
	require.NoError(t, err)

	// Create the next user message (the one that interrupted the tool call).
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: "Fix #2"},
		},
	})
	require.NoError(t, err)

	msgs, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	history, _ := agent.preparePrompt(msgs, true)

	// The history must contain a synthetic tool result for the orphaned call.
	found := false
	for _, msg := range history {
		if msg.Role != fantasy.MessageRoleTool {
			continue
		}
		for _, part := range msg.Content {
			if tr, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part); ok {
				if tr.ToolCallID == "call_orphaned_1" {
					found = true
					_, isError := tr.Output.(fantasy.ToolResultOutputContentError)
					require.True(t, isError, "orphaned tool result should be an error")
				}
			}
		}
	}
	require.True(t, found, "expected synthetic tool result for orphaned tool call")
}

func TestPreparePrompt_OrphanedToolUseMixed(t *testing.T) {
	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "test prompt")
	agent := sa.(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "test")
	require.NoError(t, err)

	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: "hello"},
		},
	})
	require.NoError(t, err)

	// Assistant with 2 tool calls: one has a result, one is orphaned.
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{
				ID:       "call_ok",
				Name:     "view",
				Input:    `{"path":"/foo"}`,
				Finished: true,
			},
			message.ToolCall{
				ID:       "call_orphaned",
				Name:     "agent",
				Input:    `{"prompt":"search"}`,
				Finished: true,
			},
		},
	})
	require.NoError(t, err)

	// Only one tool result — for call_ok.
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Tool,
		Parts: []message.ContentPart{
			message.ToolResult{
				ToolCallID: "call_ok",
				Name:       "view",
				Content:    "file contents",
			},
		},
	})
	require.NoError(t, err)

	msgs, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	history, _ := agent.preparePrompt(msgs, true)

	// Should have a synthetic result only for the orphaned call.
	var syntheticCount int
	for _, msg := range history {
		if msg.Role != fantasy.MessageRoleTool {
			continue
		}
		for _, part := range msg.Content {
			if tr, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part); ok {
				if tr.ToolCallID == "call_orphaned" {
					syntheticCount++
				}
			}
		}
	}
	require.Equal(t, 1, syntheticCount, "expected exactly one synthetic result for the orphaned call")
}

// requireToolCallAdjacency asserts that every assistant message in history
// with tool calls is immediately followed by a tool message that responds to
// each of those calls, with no other message in between. This is what
// strict-adjacency providers (e.g. Kimi, DeepSeek) require.
func requireToolCallAdjacency(t *testing.T, history []fantasy.Message) {
	t.Helper()
	for i, msg := range history {
		if msg.Role != fantasy.MessageRoleAssistant {
			continue
		}
		var callIDs []string
		for _, part := range msg.Content {
			if tc, ok := fantasy.AsMessagePart[fantasy.ToolCallPart](part); ok {
				callIDs = append(callIDs, tc.ToolCallID)
			}
		}
		if len(callIDs) == 0 {
			continue
		}
		require.Less(t, i+1, len(history), "assistant with tool calls must be followed by a tool message")
		next := history[i+1]
		require.Equal(t, fantasy.MessageRoleTool, next.Role,
			"assistant with tool calls %v must be immediately followed by a tool message, got %q", callIDs, next.Role)
		responded := make(map[string]bool, len(callIDs))
		for _, part := range next.Content {
			if tr, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part); ok {
				responded[tr.ToolCallID] = true
			}
		}
		for _, id := range callIDs {
			require.True(t, responded[id],
				"tool result for call %q must immediately follow its assistant message", id)
		}
	}
}

func TestPreparePrompt_NonAdjacentToolResults(t *testing.T) {
	// A user message written between an assistant's tool call and its
	// result (e.g. resuming while a tool is still running) must not end up
	// between the two in the built history.
	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "test prompt")
	agent := sa.(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "test")
	require.NoError(t, err)

	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: "run commands"},
		},
	})
	require.NoError(t, err)

	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{
				ID:       "call_A",
				Name:     "shell",
				Input:    `{"command":"date"}`,
				Finished: true,
			},
			message.ToolCall{
				ID:       "call_B",
				Name:     "shell",
				Input:    `{"command":"uptime"}`,
				Finished: true,
			},
		},
	})
	require.NoError(t, err)

	// Interleaved user message written while the tools were still running.
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: "are we done?"},
		},
	})
	require.NoError(t, err)

	// Results arrive late, after the interleaved user message.
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Tool,
		Parts: []message.ContentPart{
			message.ToolResult{
				ToolCallID: "call_A",
				Name:       "shell",
				Content:    "Fri May 2 21:00:00 UTC 2026",
			},
		},
	})
	require.NoError(t, err)

	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Tool,
		Parts: []message.ContentPart{
			message.ToolResult{
				ToolCallID: "call_B",
				Name:       "shell",
				Content:    "21:00  up 3 days",
			},
		},
	})
	require.NoError(t, err)

	msgs, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	require.Equal(t, message.User, msgs[2].Role, "interleaved user should be between assistant and results in DB order")

	history, _ := agent.preparePrompt(msgs, false)

	requireToolCallAdjacency(t, history)

	// The interleaved user message must still be present, after the results.
	var foundInterleaved bool
	for _, msg := range history {
		if msg.Role != fantasy.MessageRoleUser {
			continue
		}
		for _, part := range msg.Content {
			if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok && text.Text == "are we done?" {
				foundInterleaved = true
			}
		}
	}
	require.True(t, foundInterleaved, "interleaved user message must not be dropped")
}

func TestPreparePrompt_ResultBeforeAssistant(t *testing.T) {
	// A tool result written before its assistant message (e.g. concurrent
	// writes) must be emitted after the assistant, exactly once, not at its
	// stored position.
	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "test prompt")
	agent := sa.(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "test")
	require.NoError(t, err)

	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Tool,
		Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "call_X", Name: "shell", Content: "result"},
		},
	})
	require.NoError(t, err)

	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{ID: "call_X", Name: "shell", Input: `{}`, Finished: true},
		},
	})
	require.NoError(t, err)

	msgs, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	history, _ := agent.preparePrompt(msgs, false)

	requireToolCallAdjacency(t, history)

	resultCount := 0
	for _, msg := range history {
		if msg.Role != fantasy.MessageRoleTool {
			continue
		}
		for _, part := range msg.Content {
			if tr, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part); ok && tr.ToolCallID == "call_X" {
				resultCount++
			}
		}
	}
	require.Equal(t, 1, resultCount, "result must be emitted exactly once")
}

func TestPreparePrompt_BundledResultsAcrossAssistants(t *testing.T) {
	// A single tool message can hold results for calls issued by different
	// assistant messages. Each assistant must be followed by its own
	// results, and no result may be emitted twice.
	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "test prompt")
	agent := sa.(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "test")
	require.NoError(t, err)

	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{ID: "call_1", Name: "shell", Input: `{}`, Finished: true},
		},
	})
	require.NoError(t, err)

	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: "and now?"},
		},
	})
	require.NoError(t, err)

	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{ID: "call_2", Name: "view", Input: `{"path":"/foo"}`, Finished: true},
		},
	})
	require.NoError(t, err)

	// Both results land in the same tool message, out of order.
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Tool,
		Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "call_2", Name: "view", Content: "file contents"},
			message.ToolResult{ToolCallID: "call_1", Name: "shell", Content: "output"},
		},
	})
	require.NoError(t, err)

	msgs, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	history, _ := agent.preparePrompt(msgs, false)

	requireToolCallAdjacency(t, history)

	counts := make(map[string]int)
	for _, msg := range history {
		if msg.Role != fantasy.MessageRoleTool {
			continue
		}
		for _, part := range msg.Content {
			if tr, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part); ok {
				counts[tr.ToolCallID]++
			}
		}
	}
	require.Equal(t, map[string]int{"call_1": 1, "call_2": 1}, counts,
		"each result must be emitted exactly once, next to its assistant")
}

func TestPreparePrompt_DropsOrphanedToolResults(t *testing.T) {
	// A tool result whose call is not in the history (e.g. the assistant
	// message was cut off by a session summary) must be dropped instead of
	// producing an unanswerable tool message.
	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "test prompt")
	agent := sa.(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "test")
	require.NoError(t, err)

	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Tool,
		Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "call_gone", Name: "shell", Content: "output"},
		},
	})
	require.NoError(t, err)

	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: "hello"},
		},
	})
	require.NoError(t, err)

	msgs, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	history, _ := agent.preparePrompt(msgs, false)

	for _, msg := range history {
		require.NotEqual(t, fantasy.MessageRoleTool, msg.Role, "orphaned tool results must be dropped")
	}
}

func TestWorkaroundProviderMediaLimitations_TextOnlyModel(t *testing.T) {
	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "test prompt")
	agent := sa.(*sessionAgent)

	pngBase64 := base64.StdEncoding.EncodeToString([]byte("fake-png-data"))

	messages := []fantasy.Message{
		{
			Role: fantasy.MessageRoleTool,
			Content: []fantasy.MessagePart{
				fantasy.ToolResultPart{
					ToolCallID: "call_1",
					Output: fantasy.ToolResultOutputContentMedia{
						Data:      pngBase64,
						MediaType: "image/png",
					},
				},
			},
		},
	}

	// Non-Anthropic provider, no image support — should replace media with
	// a text placeholder and not create a synthetic user message.
	largeModel := Model{
		ModelCfg: config.SelectedModel{Provider: "openai"},
		CatalogCfg: catalog.Model{
			SupportsImages: false,
		},
	}

	result := agent.workaroundProviderMediaLimitations(messages, largeModel)

	// Should produce exactly one message: the tool message with a text
	// placeholder. No synthetic user message with FilePart.
	require.Len(t, result, 1)
	require.Equal(t, fantasy.MessageRoleTool, result[0].Role)

	tr, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](result[0].Content[0])
	require.True(t, ok)
	_, ok = fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentText](tr.Output)
	require.True(t, ok)
}

func TestWorkaroundProviderMediaLimitations_VisionModel(t *testing.T) {
	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "test prompt")
	agent := sa.(*sessionAgent)

	pngBase64 := base64.StdEncoding.EncodeToString([]byte("fake-png-data"))

	messages := []fantasy.Message{
		{
			Role: fantasy.MessageRoleTool,
			Content: []fantasy.MessagePart{
				fantasy.ToolResultPart{
					ToolCallID: "call_1",
					Output: fantasy.ToolResultOutputContentMedia{
						Data:      pngBase64,
						MediaType: "image/png",
					},
				},
			},
		},
	}

	// Non-Anthropic provider, image support — should create a synthetic
	// user message with FilePart.
	largeModel := Model{
		ModelCfg: config.SelectedModel{Provider: "openai"},
		CatalogCfg: catalog.Model{
			SupportsImages: true,
		},
	}

	result := agent.workaroundProviderMediaLimitations(messages, largeModel)

	// Should produce two messages: tool message with placeholder text,
	// and synthetic user message with FilePart.
	require.Len(t, result, 2)
	require.Equal(t, fantasy.MessageRoleTool, result[0].Role)
	require.Equal(t, fantasy.MessageRoleUser, result[1].Role)

	// The tool message should have text placeholder.
	tr, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](result[0].Content[0])
	require.True(t, ok)
	textOutput, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentText](tr.Output)
	require.True(t, ok)
	require.Contains(t, textOutput.Text, "see attached file")

	// The synthetic user message should contain a TextPart and a FilePart.
	require.Len(t, result[1].Content, 2)
	file, ok := fantasy.AsMessagePart[fantasy.FilePart](result[1].Content[1])
	require.True(t, ok)
	require.Equal(t, "image/png", file.MediaType)
}

func TestWorkaroundProviderMediaLimitations_AnthropicProvider(t *testing.T) {
	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "test prompt")
	agent := sa.(*sessionAgent)

	pngBase64 := base64.StdEncoding.EncodeToString([]byte("fake-png-data"))

	messages := []fantasy.Message{
		{
			Role: fantasy.MessageRoleTool,
			Content: []fantasy.MessagePart{
				fantasy.ToolResultPart{
					ToolCallID: "call_1",
					Output: fantasy.ToolResultOutputContentMedia{
						Data:      pngBase64,
						MediaType: "image/png",
					},
				},
			},
		},
	}

	// Anthropic provider — should return messages unchanged regardless of
	// SupportsImages, since Anthropic handles media in tool results natively.
	largeModel := Model{
		ModelCfg: config.SelectedModel{Provider: string(catalog.InferenceProviderAnthropic)},
		CatalogCfg: catalog.Model{
			SupportsImages: true,
		},
	}

	result := agent.workaroundProviderMediaLimitations(messages, largeModel)
	require.Len(t, result, 1)
	require.Equal(t, fantasy.MessageRoleTool, result[0].Role)

	// The media should still be in the tool result, untouched.
	tr, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](result[0].Content[0])
	require.True(t, ok)
	media, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentMedia](tr.Output)
	require.True(t, ok)
	require.Equal(t, "image/png", media.MediaType)
}

func TestProviderRetryLogFields(t *testing.T) {
	t.Run("nil provider error", func(t *testing.T) {
		fields := providerRetryLogFields(nil, 2*time.Second)
		require.Equal(t, []any{"retry_delay", "2s"}, fields)
	})

	t.Run("provider error with title and message", func(t *testing.T) {
		fields := providerRetryLogFields(&fantasy.ProviderError{
			StatusCode: 429,
			Title:      "rate limit",
			Message:    "too many requests",
		}, 1500*time.Millisecond)
		require.Equal(t, []any{
			"retry_delay", "1.5s",
			"status_code", 429,
			"title", "rate limit",
			"message", "too many requests",
		}, fields)
	})

	t.Run("provider error without optional strings", func(t *testing.T) {
		fields := providerRetryLogFields(&fantasy.ProviderError{
			StatusCode: 503,
		}, time.Second)
		require.Equal(t, []any{
			"retry_delay", "1s",
			"status_code", 503,
		}, fields)
	})
}

// TestBuildSummaryPrompt covers the /compact focus: the instructions the
// arguments dialog collects have to reach the summary prompt, and an empty
// focus still has to ask for a general summary.
func TestBuildSummaryPrompt(t *testing.T) {
	t.Parallel()

	t.Run("no focus asks for a general summary", func(t *testing.T) {
		t.Parallel()

		prompt := buildSummaryPrompt("")
		assert.Equal(t, "Provide a detailed summary of our conversation above.", prompt)
	})

	t.Run("blank focus is not a focus", func(t *testing.T) {
		t.Parallel()

		assert.NotContains(t, buildSummaryPrompt("   \n\t "), "## Focus")
	})

	t.Run("focus reaches the prompt", func(t *testing.T) {
		t.Parallel()

		prompt := buildSummaryPrompt("  keep the auth work  ")
		assert.Contains(t, prompt, "## Focus\n\nkeep the auth work\n")
	})
}

// downloadedPath pulls the saved path out of a fetch download result,
// which reads "Downloaded N bytes from <url> to <path> (Content-Type: …)".
func downloadedPath(t *testing.T, content string) string {
	t.Helper()
	_, after, found := strings.Cut(content, " to ")
	require.True(t, found, "no path in download result: %s", content)
	path, _, _ := strings.Cut(after, " (Content-Type:")
	return strings.TrimSpace(strings.SplitN(path, "\n", 2)[0])
}
