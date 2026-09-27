package message

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShouldFlushNowToolCallBaseline(t *testing.T) {
	t.Parallel()

	newMsg := func(calls ...ToolCall) *Message {
		msg := &Message{}
		for _, call := range calls {
			msg.AddToolCall(call)
		}
		return msg
	}
	baseline := func(msg *Message) *flushBaseline {
		b := newFlushBaseline(msg)
		return &b
	}

	t.Run("same baseline is coalesced", func(t *testing.T) {
		t.Parallel()
		msg := newMsg(ToolCall{ID: "a", Finished: true}, ToolCall{ID: "b", Finished: false})
		prev := baseline(msg)
		require.False(t, shouldFlushNow(prev, newMsg(ToolCall{ID: "a", Finished: true}, ToolCall{ID: "b", Finished: false})))
	})

	t.Run("finish flip flushes", func(t *testing.T) {
		t.Parallel()
		prev := baseline(newMsg(ToolCall{ID: "a", Finished: false}))
		require.True(t, shouldFlushNow(prev, newMsg(ToolCall{ID: "a", Finished: true})))
	})

	t.Run("positional mismatch flushes", func(t *testing.T) {
		t.Parallel()
		prev := baseline(newMsg(ToolCall{ID: "a", Finished: true}, ToolCall{ID: "b", Finished: false}))
		require.True(t, shouldFlushNow(prev, newMsg(ToolCall{ID: "a", Finished: false}, ToolCall{ID: "b", Finished: false})))
	})

	t.Run("count growth flushes", func(t *testing.T) {
		t.Parallel()
		prev := baseline(newMsg(ToolCall{ID: "a", Finished: true}))
		require.True(t, shouldFlushNow(prev, newMsg(ToolCall{ID: "a", Finished: true}, ToolCall{ID: "b", Finished: false})))
	})

	t.Run("count shrink flushes", func(t *testing.T) {
		t.Parallel()
		prev := baseline(newMsg(ToolCall{ID: "a", Finished: true}, ToolCall{ID: "b", Finished: true}))
		require.True(t, shouldFlushNow(prev, newMsg(ToolCall{ID: "a", Finished: true})))
	})

	t.Run("nil baseline flushes on any call", func(t *testing.T) {
		t.Parallel()
		require.True(t, shouldFlushNow(nil, newMsg(ToolCall{ID: "a", Finished: false})))
	})
}
