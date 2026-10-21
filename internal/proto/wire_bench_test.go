package proto

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/pubsub"
)

func streamingMessage(size int) pubsub.Event[message.Message] {
	chunk := strings.Repeat("streamed <token> & \"quoted\" text\n", size/64+1)
	return pubsub.Event[message.Message]{
		Type: pubsub.UpdatedEvent,
		Payload: message.Message{
			ID:        "m1",
			SessionID: "s1",
			Role:      message.Assistant,
			Model:     "model-x",
			Provider:  "p",
			CreatedAt: 1,
			UpdatedAt: 2,
			Parts: []message.ContentPart{
				message.ReasoningContent{Thinking: chunk[:size/4], StartedAt: 1},
				message.TextContent{Text: chunk[:size/4]},
				message.ToolCall{ID: "c1", Name: "view", Input: `{"path":"/tmp/x"}`, Finished: true},
				message.ToolResult{ToolCallID: "c1", Name: "view", Content: chunk[:size/2], Metadata: `{"k":1}`},
			},
		},
	}
}

func legacyFrame(t *testing.T, ev pubsub.Event[Message]) string {
	t.Helper()
	type wrapper struct {
		Type partType    `json:"type"`
		Data ContentPart `json:"data"`
	}
	wrapped := make([]wrapper, len(ev.Payload.Parts))
	for i, part := range ev.Payload.Parts {
		wrapped[i] = wrapper{Type: part.partType(), Data: part}
	}
	parts, err := json.Marshal(wrapped)
	require.NoError(t, err)
	type alias Message
	msg, err := json.Marshal(&struct {
		Parts json.RawMessage `json:"parts"`
		*alias
	}{Parts: parts, alias: (*alias)(&ev.Payload)})
	require.NoError(t, err)
	payload, err := json.Marshal(struct {
		Type    pubsub.EventType `json:"type"`
		Payload json.RawMessage  `json:"payload"`
	}{Type: ev.Type, Payload: msg})
	require.NoError(t, err)
	return `{"type":"message","payload":` + string(payload) + `}`
}

func TestMessageFrameMatchesLegacyEncoding(t *testing.T) {
	t.Parallel()
	ev := streamingMessage(512)
	ev.Payload.Parts = append(ev.Payload.Parts,
		message.TextContent{Text: "invalid \xff utf8 \u2028 <b>"},
		message.BinaryContent{Path: "/a.png", MIMEType: "image/png", Data: []byte{0, 1, 2}},
		message.Finish{Reason: message.FinishReasonEndTurn, Time: 3},
	)
	frame, err := EncodeEvent(ev)
	require.NoError(t, err)
	wire := pubsub.Event[Message]{Type: ev.Type, Payload: MessageFromDomain(ev.Payload)}
	require.Equal(t, legacyFrame(t, wire), string(frame))

	decoded, err := DecodeEvent(frame)
	require.NoError(t, err)
	stdlib, err := json.Marshal(wire.Payload)
	require.NoError(t, err)
	var viaStdlib Message
	require.NoError(t, json.Unmarshal(stdlib, &viaStdlib))
	require.Equal(t, decoded.(pubsub.Event[Message]).Payload, viaStdlib)
	require.Equal(t, "invalid \uFFFD utf8 \u2028 <b>", viaStdlib.Parts[4].(TextContent).Text)
}

func TestEmptyPartsRoundTripAsEmpty(t *testing.T) {
	t.Parallel()
	frame, err := EncodeEvent(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: message.Message{ID: "m1"}})
	require.NoError(t, err)
	require.Contains(t, string(frame), `"parts":[]`)
	wire, err := DecodeEvent(frame)
	require.NoError(t, err)
	require.Equal(t, []ContentPart{}, wire.(pubsub.Event[Message]).Payload.Parts)
}

func BenchmarkSSEMessageRoundTrip(b *testing.B) {
	for _, size := range []int{4 << 10, 64 << 10} {
		ev := streamingMessage(size)
		b.Run(fmt.Sprintf("%dKB", size>>10), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				frame, err := EncodeEvent(ev)
				if err != nil {
					b.Fatal(err)
				}
				wire, err := DecodeEvent(frame)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := EventToDomain(wire); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
