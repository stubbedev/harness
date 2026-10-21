package message

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	benchRoundTrips  = 160
	benchResultBytes = 10 << 10
)

func seedLongSession(tb testing.TB, svc Service, sessionID string) {
	tb.Helper()
	ctx := tb.Context()
	_, err := svc.Create(ctx, sessionID, CreateMessageParams{
		Role:  User,
		Parts: []ContentPart{TextContent{Text: "Refactor the storage layer and keep the tests green."}},
	})
	require.NoError(tb, err)
	result := strings.Repeat("func example() { return \"line\\twith \\\"escapes\\\"\" }\n", benchResultBytes/52)
	for i := range benchRoundTrips {
		callID := fmt.Sprintf("call-%d", i)
		_, err := svc.Create(ctx, sessionID, CreateMessageParams{
			Role: Assistant,
			Parts: []ContentPart{
				ReasoningContent{Thinking: strings.Repeat("considering the next step ", 40), Signature: "sig", StartedAt: 1, FinishedAt: 2},
				TextContent{Text: "Reading the next file."},
				ToolCall{ID: callID, Name: "view", Input: fmt.Sprintf(`{"file_path":"/repo/internal/pkg%d/file.go","offset":0,"limit":200}`, i), Finished: true},
				Finish{Reason: FinishReasonToolUse, Time: int64(i)},
			},
			Model:    "model",
			Provider: "provider",
		})
		require.NoError(tb, err)
		_, err = svc.Create(ctx, sessionID, CreateMessageParams{
			Role:  Tool,
			Parts: []ContentPart{ToolResult{ToolCallID: callID, Name: "view", Content: result, Metadata: `{"lines":200}`}},
		})
		require.NoError(tb, err)
	}
}

func BenchmarkListSession(b *testing.B) {
	svc, sessionID := newTestService(b)
	seedLongSession(b, svc, sessionID)
	ctx := b.Context()
	for b.Loop() {
		msgs, err := svc.List(ctx, sessionID)
		if err != nil {
			b.Fatal(err)
		}
		if len(msgs) != 2*benchRoundTrips+1 {
			b.Fatalf("listed %d messages", len(msgs))
		}
	}
}

func BenchmarkUnmarshalParts(b *testing.B) {
	svc, sessionID := newTestService(b)
	seedLongSession(b, svc, sessionID)
	msgs, err := svc.List(b.Context(), sessionID)
	require.NoError(b, err)
	stored := make([][]byte, len(msgs))
	var size int64
	for i, m := range msgs {
		stored[i], err = marshalParts(m.Parts)
		require.NoError(b, err)
		size += int64(len(stored[i]))
	}
	b.SetBytes(size)
	for b.Loop() {
		for _, data := range stored {
			if _, err := unmarshalParts(data); err != nil {
				b.Fatal(err)
			}
		}
	}
}
