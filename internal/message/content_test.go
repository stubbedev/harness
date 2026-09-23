package message

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func makeTestAttachments(n int, contentSize int) []Attachment {
	attachments := make([]Attachment, n)
	content := []byte(strings.Repeat("x", contentSize))
	for i := range n {
		attachments[i] = Attachment{
			FilePath: fmt.Sprintf("/path/to/file%d.txt", i),
			MimeType: "text/plain",
			Content:  content,
		}
	}
	return attachments
}

func TestToAIMessage_SubagentNoteReadsAsUserText(t *testing.T) {
	t.Parallel()

	note := SubagentNote{AgentName: "researcher", Handle: "bg-1", ChildSessionID: "c1", Text: "halfway there"}
	require.Equal(t, "[Message from background agent \"researcher\" (handle bg-1)]\nhalfway there", note.String())

	msg := &Message{
		Role:  User,
		Parts: []ContentPart{note},
	}
	messages := msg.ToAIMessage()
	require.Len(t, messages, 1)
	require.Len(t, messages[0].Content, 1)
	text, ok := messages[0].Content[0].(fantasy.TextPart)
	require.True(t, ok)
	require.Equal(t, note.String(), text.Text)
	require.Len(t, msg.SubagentNotes(), 1)
}

// TestSubagentNotesOnly pins the persisted report-back shape: Create
// appends a Finish bookkeeping part to every non-assistant message, so
// a note-only user message carries [SubagentNote, Finish]. Renderers
// rely on SubagentNotesOnly to skip it instead of drawing an empty
// user bubble.
func TestSubagentNotesOnly(t *testing.T) {
	t.Parallel()

	note := SubagentNote{AgentName: "researcher", Text: "halfway there"}
	require.False(t, (&Message{Role: User, Parts: nil}).SubagentNotesOnly())
	require.False(t, (&Message{Role: User, Parts: []ContentPart{Finish{Reason: "stop"}}}).SubagentNotesOnly())
	require.True(t, (&Message{Role: User, Parts: []ContentPart{note, Finish{Reason: "stop"}}}).SubagentNotesOnly())
	require.True(t, (&Message{Role: User, Parts: []ContentPart{note}}).SubagentNotesOnly())
	require.False(t, (&Message{Role: User, Parts: []ContentPart{note, TextContent{Text: "and I say"}}}).SubagentNotesOnly())
}

func TestToAIMessage_CorruptedMediaData(t *testing.T) {
	t.Parallel()

	msg := &Message{
		Role: Tool,
		Parts: []ContentPart{
			ToolResult{
				ToolCallID: "call_123",
				Name:       "screenshot",
				Content:    "Loaded image/png content",
				Data:       "abc\x80def",
				MIMEType:   "image/png",
			},
		},
	}

	messages := msg.ToAIMessage()
	require.Len(t, messages, 1)
	require.Len(t, messages[0].Content, 1)

	part, ok := messages[0].Content[0].(fantasy.ToolResultPart)
	require.True(t, ok)

	require.Equal(t, "call_123", part.ToolCallID)

	textContent, ok := part.Output.(fantasy.ToolResultOutputContentText)
	require.True(t, ok, "corrupted media should be downgraded to text")
	require.Equal(t, mediaLoadFailedPlaceholder, textContent.Text)
}

func TestToAIMessage_ValidMediaData(t *testing.T) {
	t.Parallel()

	validBase64 := base64.StdEncoding.EncodeToString([]byte{0x89, 0x50, 0x4E, 0x47})

	msg := &Message{
		Role: Tool,
		Parts: []ContentPart{
			ToolResult{
				ToolCallID: "call_456",
				Name:       "screenshot",
				Content:    "Loaded image/png content",
				Data:       validBase64,
				MIMEType:   "image/png",
			},
		},
	}

	messages := msg.ToAIMessage()
	require.Len(t, messages, 1)
	require.Len(t, messages[0].Content, 1)

	part, ok := messages[0].Content[0].(fantasy.ToolResultPart)
	require.True(t, ok)

	require.Equal(t, "call_456", part.ToolCallID)

	mediaContent, ok := part.Output.(fantasy.ToolResultOutputContentMedia)
	require.True(t, ok, "valid media should remain as media")
	require.Equal(t, validBase64, mediaContent.Data)
	require.Equal(t, "image/png", mediaContent.MediaType)
}

func TestToAIMessage_ASCIIButInvalidBase64(t *testing.T) {
	t.Parallel()

	msg := &Message{
		Role: Tool,
		Parts: []ContentPart{
			ToolResult{
				ToolCallID: "call_789",
				Name:       "screenshot",
				Content:    "Loaded image/png content",
				Data:       "not-valid-base64!!!",
				MIMEType:   "image/png",
			},
		},
	}

	messages := msg.ToAIMessage()
	require.Len(t, messages, 1)
	require.Len(t, messages[0].Content, 1)

	part, ok := messages[0].Content[0].(fantasy.ToolResultPart)
	require.True(t, ok)

	require.Equal(t, "call_789", part.ToolCallID)

	textContent, ok := part.Output.(fantasy.ToolResultOutputContentText)
	require.True(t, ok, "ASCII but invalid base64 should be downgraded to text")
	require.Equal(t, mediaLoadFailedPlaceholder, textContent.Text)
}

func BenchmarkPromptWithTextAttachments(b *testing.B) {
	cases := []struct {
		name        string
		numFiles    int
		contentSize int
	}{
		{"1file_100bytes", 1, 100},
		{"5files_1KB", 5, 1024},
		{"10files_10KB", 10, 10 * 1024},
		{"20files_50KB", 20, 50 * 1024},
	}

	for _, tc := range cases {
		attachments := makeTestAttachments(tc.numFiles, tc.contentSize)
		prompt := "Process these files"

		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				_ = PromptWithTextAttachments(prompt, attachments)
			}
		})
	}
}

func TestResetStreamedContent(t *testing.T) {
	t.Parallel()

	msg := &Message{}
	msg.AddImageURL("https://example.com/img.png", "high")
	msg.AppendContent("partial answer")
	msg.AppendReasoningContent("thinking...")
	msg.AddToolCall(ToolCall{ID: "1", Name: "shell"})
	msg.AddToolResult(ToolResult{ToolCallID: "1", Content: "output"})
	msg.AddFinish(FinishReasonError, "boom", "stream died")

	msg.ResetStreamedContent()

	// Streamed parts are gone.
	require.Empty(t, msg.Content().Text, "text should be cleared")
	require.Empty(t, msg.ReasoningContent().Thinking, "reasoning should be cleared")
	require.Empty(t, msg.ToolCalls(), "tool calls should be cleared")
	require.Nil(t, msg.FinishPart(), "finish should be cleared")

	// Non-streamed parts survive.
	require.Len(t, msg.ImageURLContent(), 1, "image should survive")
	require.Len(t, msg.ToolResults(), 1, "tool results should survive")
}

func TestResetStreamedContentEmpty(t *testing.T) {
	t.Parallel()

	// Reset on an empty message is a no-op and must not panic.
	msg := &Message{}
	msg.ResetStreamedContent()
	require.Empty(t, msg.Parts)
}

// TestAppendContent_OnlyAppendsToFirstMatch pins the defensive
// single-match behavior of AppendContent: a delta must extend exactly
// one TextContent part, even when Parts contains more than one. Without
// the return after a match, the loop continues and appends `delta` to
// every TextContent in Parts, quietly multiplying the visible response.
// The sibling helpers in this file (AppendThoughtSignature,
// FinishThinking) all single-match; this test guards that AppendContent
// and AppendReasoningContent stay in the same family.
func TestAppendContent_OnlyAppendsToFirstMatch(t *testing.T) {
	t.Parallel()

	m := &Message{
		Parts: []ContentPart{
			TextContent{Text: "first"},
			TextContent{Text: "second"},
		},
	}

	m.AppendContent(" delta")

	require.Len(t, m.Parts, 2)
	require.Equal(t, "first delta", m.Parts[0].(TextContent).Text)
	require.Equal(t, "second", m.Parts[1].(TextContent).Text,
		"second TextContent part must be untouched; AppendContent multi-matched")
}

func TestAppendContent_CreatesPartWhenAbsent(t *testing.T) {
	t.Parallel()

	m := &Message{Parts: []ContentPart{ToolCall{ID: "tc1"}}}
	m.AppendContent("hello")

	require.Len(t, m.Parts, 2)
	tc, ok := m.Parts[1].(TextContent)
	require.True(t, ok, "expected TextContent appended after ToolCall")
	require.Equal(t, "hello", tc.Text)
}

func TestAppendReasoningContent_OnlyAppendsToFirstMatch(t *testing.T) {
	t.Parallel()

	m := &Message{
		Parts: []ContentPart{
			ReasoningContent{Thinking: "a", StartedAt: 1},
			ReasoningContent{Thinking: "b", StartedAt: 2},
		},
	}

	m.AppendReasoningContent(" delta")

	require.Len(t, m.Parts, 2)
	require.Equal(t, "a delta", m.Parts[0].(ReasoningContent).Thinking)
	require.Equal(t, "b", m.Parts[1].(ReasoningContent).Thinking,
		"second ReasoningContent part must be untouched")
}

// TestAppendContentMatchesConcatenation pins the builder fast path against
// the obvious implementation it replaced.
func TestAppendContentMatchesConcatenation(t *testing.T) {
	t.Parallel()

	msg := &Message{Role: Assistant}
	var want string
	for i := range 500 {
		delta := fmt.Sprintf("delta-%d ", i)
		want += delta
		msg.AppendContent(delta)
		require.Equal(t, want, msg.Content().Text, "after delta %d", i)
	}
}

// TestAppendContentCloneIsASnapshot is the invariant the shared builder
// could break: a clone handed to the writer must not change under it while
// the original keeps streaming.
func TestAppendContentCloneIsASnapshot(t *testing.T) {
	t.Parallel()

	msg := &Message{Role: Assistant}
	for range 100 {
		msg.AppendContent("chunk ")
	}

	snapshot := msg.Clone()
	frozen := snapshot.Content().Text

	for range 100 {
		msg.AppendContent("more ")
	}

	require.Equal(t, frozen, snapshot.Content().Text, "clone changed under the writer")
	require.NotEqual(t, frozen, msg.Content().Text, "original should have grown")

	// The clone must also be safe to append to on its own.
	snapshot.AppendContent("!")
	require.Equal(t, frozen+"!", snapshot.Content().Text)
	require.False(t, strings.HasSuffix(msg.Content().Text, "!"), "writing to the clone leaked into the original")
}

// TestAppendContentAfterReset covers the retry path: ResetStreamedContent
// drops the text part, and the deltas that follow must not be appended onto
// the text that was thrown away.
func TestAppendContentAfterReset(t *testing.T) {
	t.Parallel()

	msg := &Message{Role: Assistant}
	msg.AppendContent("first attempt, quite a long one")
	msg.ResetStreamedContent()

	msg.AppendContent("second ")
	msg.AppendContent("attempt")
	require.Equal(t, "second attempt", msg.Content().Text)
}

// TestAppendContentAfterExternalRewrite covers a part replaced by something
// that knows nothing about the builder, such as a message read back from the
// database.
func TestAppendContentAfterExternalRewrite(t *testing.T) {
	t.Parallel()

	msg := &Message{Role: Assistant}
	msg.AppendContent("streamed")

	msg.Parts = []ContentPart{TextContent{Text: "replaced"}}
	msg.AppendContent(" and grown")
	require.Equal(t, "replaced and grown", msg.Content().Text)
}

// TestAppendReasoningContentPreservesFields guards the fields the old
// implementation dropped: it rebuilt ReasoningContent from four named
// fields, so anything recorded mid-stream was erased by the next delta.
func TestAppendReasoningContentPreservesFields(t *testing.T) {
	t.Parallel()

	msg := &Message{Role: Assistant}
	msg.AppendReasoningContent("thinking ")

	reasoning := msg.ReasoningContent()
	reasoning.ThoughtSignature = "sig-abc"
	reasoning.ToolID = "tool-1"
	reasoning.Signature = "outer-sig"
	msg.Parts[0] = reasoning

	msg.AppendReasoningContent("more")

	got := msg.ReasoningContent()
	require.Equal(t, "thinking more", got.Thinking)
	require.Equal(t, "sig-abc", got.ThoughtSignature)
	require.Equal(t, "tool-1", got.ToolID)
	require.Equal(t, "outer-sig", got.Signature)
}

func TestToAIMessage_ContextNoteReadsAsUserText(t *testing.T) {
	t.Parallel()

	note := ContextNote{Kind: ContextNoteRuntime, Text: "<harness_runtime>\nenv\n</harness_runtime>"}
	msg := &Message{Role: User, Parts: []ContentPart{note, Finish{Reason: "stop"}}}
	messages := msg.ToAIMessage()
	require.Len(t, messages, 1)
	require.Len(t, messages[0].Content, 1)
	text, ok := messages[0].Content[0].(fantasy.TextPart)
	require.True(t, ok)
	require.Equal(t, note.Text, text.Text, "the note is sent verbatim, so it matches the message it was first sent as")
	require.True(t, msg.ContextNotesOnly())
	require.False(t, (&Message{Role: User, Parts: []ContentPart{note, TextContent{Text: "typed"}}}).ContextNotesOnly())
	require.False(t, (&Message{Role: User, Parts: []ContentPart{Finish{Reason: "stop"}}}).ContextNotesOnly())

	data, err := marshalParts(msg.Parts)
	require.NoError(t, err)
	parts, err := unmarshalParts(data)
	require.NoError(t, err)
	require.Equal(t, note, parts[0])
}

// TestUnmarshalParts_SkipsUnknownType: a part type written by a newer
// build must not make the whole message (and so the session) unloadable.
func TestUnmarshalParts_SkipsUnknownType(t *testing.T) {
	t.Parallel()

	data := []byte(`[{"type":"hologram","data":{}},{"type":"text","data":{"text":"hi"}}]`)
	parts, err := unmarshalParts(data)
	require.NoError(t, err)
	require.Equal(t, []ContentPart{TextContent{Text: "hi"}}, parts)
}
