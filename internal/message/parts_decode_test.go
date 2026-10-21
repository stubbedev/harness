package message

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unsafe"

	"charm.land/fantasy/providers/openai"
	"github.com/stretchr/testify/require"
)

func legacyDecode[T ContentPart](t *testing.T, raw json.RawMessage) ContentPart {
	t.Helper()
	var part T
	require.NoError(t, json.Unmarshal(raw, &part))
	return part
}

func legacyUnmarshalParts(t *testing.T, data []byte) []ContentPart {
	t.Helper()
	decoders := map[partType]func(*testing.T, json.RawMessage) ContentPart{
		reasoningType:    legacyDecode[ReasoningContent],
		textType:         legacyDecode[TextContent],
		imageURLType:     legacyDecode[ImageURLContent],
		binaryType:       legacyDecode[BinaryContent],
		toolCallType:     legacyDecode[ToolCall],
		toolResultType:   legacyDecode[ToolResult],
		finishType:       legacyDecode[Finish],
		shellCommandType: legacyDecode[ShellCommand],
		subagentNoteType: legacyDecode[SubagentNote],
		contextNoteType:  legacyDecode[ContextNote],
	}
	require.Len(t, decoders, len(partDecoders))
	var wrappers []struct {
		Type partType        `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(data, &wrappers))
	parts := make([]ContentPart, 0, len(wrappers))
	for _, w := range wrappers {
		decode, ok := decoders[w.Type]
		require.True(t, ok, w.Type)
		parts = append(parts, decode(t, w.Data))
	}
	return parts
}

func everyPartKind() []ContentPart {
	return []ContentPart{
		ReasoningContent{
			Thinking: "think \"hard\"\n\u00e9", Signature: "sig", ThoughtSignature: "ts", ToolID: "tid",
			ResponsesData: &openai.ResponsesReasoningMetadata{ItemID: "item", Summary: []string{"a", "b"}},
			StartedAt:     1, FinishedAt: 2,
		},
		TextContent{Text: "hello \\ world \u2028 <tag>"},
		ImageURLContent{URL: "https://example.com/x.png", Detail: "high"},
		BinaryContent{Path: "/tmp/a.png", MIMEType: "image/png", Data: []byte{0, 1, 2, 255}},
		ToolCall{ID: "c1", Name: "view", Input: `{"file_path":"/x"}`, ProviderExecuted: true, Finished: true, MCPServer: "srv"},
		ToolResult{ToolCallID: "c1", Name: "view", Content: strings.Repeat("line\t\"q\"\n", 50), Data: "ZGF0YQ==", MIMEType: "image/png", Metadata: `{"k":1}`, IsError: true, Canceled: true},
		Finish{Reason: FinishReasonToolUse, Time: 42, Message: "m", Details: "d"},
		ShellCommand{Command: "ls", Output: "a\nb", ExitCode: 2},
		SubagentNote{AgentName: "worker", Handle: "h", ChildSessionID: "child", Text: "done"},
		ContextNote{Kind: ContextNoteRuntime, Text: "env"},
	}
}

func TestUnmarshalPartsMatchesEncodingJSON(t *testing.T) {
	t.Parallel()

	parts := everyPartKind()
	require.Len(t, parts, len(PartTypes()))
	data, err := marshalParts(parts)
	require.NoError(t, err)

	got, err := unmarshalParts(data)
	require.NoError(t, err)
	require.Equal(t, legacyUnmarshalParts(t, data), got)
}

func TestUnmarshalPartsZeroValuesMatchEncodingJSON(t *testing.T) {
	t.Parallel()

	data, err := marshalParts(PartTypes())
	require.NoError(t, err)
	got, err := unmarshalParts(data)
	require.NoError(t, err)
	require.Equal(t, legacyUnmarshalParts(t, data), got)
}

func TestUnmarshalPartsDataBeforeType(t *testing.T) {
	t.Parallel()

	data := []byte(`[{"data":{"text":"hi"},"extra":[1,{"a":null}],"type":"text"},{"type":"finish","data":{"reason":"stop","time":3}}]`)
	got, err := unmarshalParts(data)
	require.NoError(t, err)
	require.Equal(t, []ContentPart{TextContent{Text: "hi"}, Finish{Reason: "stop", Time: 3}}, got)
}

func TestUnmarshalPartsEmptyAndNull(t *testing.T) {
	t.Parallel()

	for _, data := range []string{`[]`, `null`, ` [ ] `} {
		got, err := unmarshalParts([]byte(data))
		require.NoError(t, err, data)
		require.NotNil(t, got, data)
		require.Empty(t, got, data)
	}
}

func TestUnmarshalPartsRejectsMalformed(t *testing.T) {
	t.Parallel()

	for _, data := range []string{``, `{}`, `[{"type":"text","data":{"text":1}}]`, `[{"type":"text","data":{"text":"x"}`, `[1]`, `[{"type":"text"}]`} {
		_, err := unmarshalParts([]byte(data))
		require.Error(t, err, data)
	}
}

func TestUnmarshalPartsSkipsUnknownTypeWithDataFirst(t *testing.T) {
	t.Parallel()

	data := []byte(`[{"data":{"x":1},"type":"hologram"},{"type":"text","data":{"text":"hi"}}]`)
	got, err := unmarshalParts(data)
	require.NoError(t, err)
	require.Equal(t, []ContentPart{TextContent{Text: "hi"}}, got)
}

func toolResultContent(t *testing.T, m Message) string {
	t.Helper()
	for _, part := range m.Parts {
		if tr, ok := part.(ToolResult); ok {
			return tr.Content
		}
	}
	t.Fatalf("message %s holds no tool result", m.ID)
	return ""
}

func TestListReusesDecodedPartsOfUnchangedRows(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t)
	created, err := svc.Create(t.Context(), sessionID, CreateMessageParams{
		Role:  Tool,
		Parts: []ContentPart{ToolResult{ToolCallID: "c", Name: "view", Content: strings.Repeat("x", 4096)}},
	})
	require.NoError(t, err)

	first, err := svc.List(t.Context(), sessionID)
	require.NoError(t, err)
	second, err := svc.List(t.Context(), sessionID)
	require.NoError(t, err)
	require.Equal(t, first, second)
	a, b := toolResultContent(t, first[0]), toolResultContent(t, second[0])
	require.Same(t, unsafe.StringData(a), unsafe.StringData(b), "an unchanged row is not decoded again")

	got, err := svc.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.Same(t, unsafe.StringData(a), unsafe.StringData(toolResultContent(t, got)))
}

func TestListSeesEveryUpdateWithinOneSecond(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t, WithDebounce(0))
	msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
	require.NoError(t, err)
	for i := range 5 {
		msg.Parts = []ContentPart{TextContent{Text: fmt.Sprintf("revision %d", i)}}
		require.NoError(t, svc.Update(t.Context(), msg))
		listed, err := svc.List(t.Context(), sessionID)
		require.NoError(t, err)
		require.Len(t, listed, 1)
		require.Equal(t, fmt.Sprintf("revision %d", i), listed[0].Content().Text)
	}
}

func TestListedPartsAreTheCallersOwn(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t)
	_, err := svc.Create(t.Context(), sessionID, CreateMessageParams{
		Role:  User,
		Parts: []ContentPart{TextContent{Text: "original"}},
	})
	require.NoError(t, err)

	first, err := svc.List(t.Context(), sessionID)
	require.NoError(t, err)
	first[0].Parts[0] = TextContent{Text: "mutated"}
	first[0].Parts = append(first[0].Parts, TextContent{Text: "appended"})

	second, err := svc.List(t.Context(), sessionID)
	require.NoError(t, err)
	require.Equal(t, "original", second[0].Content().Text)
	require.Len(t, second[0].Parts, 2)
}

func TestDeleteForgetsDecodedParts(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t)
	kept, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: User, Parts: []ContentPart{TextContent{Text: "kept"}}})
	require.NoError(t, err)
	gone, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: User, Parts: []ContentPart{TextContent{Text: "gone"}}})
	require.NoError(t, err)
	tail, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: User, Parts: []ContentPart{TextContent{Text: "tail"}}})
	require.NoError(t, err)
	_, err = svc.List(t.Context(), sessionID)
	require.NoError(t, err)

	decoded := svc.(*service).decoded
	require.NoError(t, svc.Delete(t.Context(), gone.ID))
	_, err = svc.DeleteFrom(t.Context(), sessionID, tail.ID)
	require.NoError(t, err)

	decoded.mu.Lock()
	defer decoded.mu.Unlock()
	require.Contains(t, decoded.entries, kept.ID)
	require.NotContains(t, decoded.entries, gone.ID)
	require.NotContains(t, decoded.entries, tail.ID)
	require.Equal(t, len(decoded.entries), decoded.order.Len())
}

func TestDecodedPartsEvictsLeastRecentlyUsedOverBudget(t *testing.T) {
	t.Parallel()

	raw := func(text string) string {
		data, err := marshalParts([]ContentPart{TextContent{Text: text}})
		require.NoError(t, err)
		return string(data)
	}
	a, b, c := raw("aaaa"), raw("bbbb"), raw("cccc")
	cache := newDecodedParts(len(a) + len(b))

	_, err := cache.decode("a", a)
	require.NoError(t, err)
	_, err = cache.decode("b", b)
	require.NoError(t, err)
	_, err = cache.decode("a", a)
	require.NoError(t, err)
	_, err = cache.decode("c", c)
	require.NoError(t, err)

	require.Contains(t, cache.entries, "a")
	require.NotContains(t, cache.entries, "b")
	require.Contains(t, cache.entries, "c")
	require.Equal(t, len(a)+len(c), cache.size)

	_, err = cache.decode("big", raw(strings.Repeat("z", 64)))
	require.NoError(t, err)
	require.NotContains(t, cache.entries, "big")
	require.Equal(t, len(a)+len(c), cache.size)

	changed := raw("AAAA")
	parts, err := cache.decode("a", changed)
	require.NoError(t, err)
	require.Equal(t, []ContentPart{TextContent{Text: "AAAA"}}, parts)
	require.Equal(t, len(changed)+len(c), cache.size)
}
