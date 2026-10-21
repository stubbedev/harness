package backend

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent/notify"
	"github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/app"
	"github.com/stubbedev/harness/internal/proto"
	"github.com/stubbedev/harness/internal/pubsub"
	"github.com/stubbedev/harness/internal/session"
)

func newFramesWorkspace(t *testing.T) *Workspace {
	t.Helper()
	a := app.NewForTest(t.Context())
	t.Cleanup(a.ShutdownForTest)
	ws := &Workspace{App: a}
	ws.ctx, ws.cancel = context.WithCancel(t.Context())
	t.Cleanup(ws.cancel)
	return ws
}

func nextFrame(t *testing.T, ch <-chan pubsub.Event[[]byte]) pubsub.Event[[]byte] {
	t.Helper()
	select {
	case ev, ok := <-ch:
		require.True(t, ok, "frame channel closed")
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no frame delivered")
		return pubsub.Event[[]byte]{}
	}
}

func TestEventFramesEncodeOnceForEveryStream(t *testing.T) {
	t.Parallel()
	ws := newFramesWorkspace(t)
	first := ws.EventFrames(t.Context())
	second := ws.EventFrames(t.Context())

	sess := pubsub.Event[session.Session]{Type: pubsub.UpdatedEvent, Payload: session.Session{ID: "s1", Title: "t"}}
	ws.SendEvent(sess)

	a, b := nextFrame(t, first), nextFrame(t, second)
	require.Equal(t, pubsub.UpdatedEvent, a.Type)
	require.Same(t, &a.Payload[0], &b.Payload[0], "every stream must share one encoding of the event")
	want, err := proto.EncodeEvent(sess)
	require.NoError(t, err)
	require.Equal(t, want, a.Payload)
}

func TestEventFramesKeepMustDeliverAndDropWirelessEvents(t *testing.T) {
	t.Parallel()
	ws := newFramesWorkspace(t)
	frames := ws.EventFrames(t.Context())

	ws.SendEvent(pubsub.Event[mcp.Event]{Payload: mcp.Event{Type: mcp.EventChannelMessage}})
	require.Eventually(t, func() bool { return ws.RunCompletions().GetSubscriberCount() > 0 }, 2*time.Second, time.Millisecond)
	ws.RunCompletions().PublishMustDeliver(t.Context(), pubsub.UpdatedEvent, notify.RunComplete{SessionID: "s1", RunID: "r1"})

	ev := nextFrame(t, frames)
	require.True(t, ev.MustDeliver)
	wire, err := proto.DecodeEvent(ev.Payload)
	require.NoError(t, err)
	require.Equal(t, "r1", wire.(pubsub.Event[proto.RunComplete]).Payload.RunID)
}

func TestEventFramesCloseWithTheWorkspace(t *testing.T) {
	t.Parallel()
	ws := newFramesWorkspace(t)
	frames := ws.EventFrames(t.Context())
	ws.cancel()
	select {
	case _, ok := <-frames:
		require.False(t, ok)
	case <-time.After(2 * time.Second):
		t.Fatal("frame channel stayed open after the workspace closed")
	}
}
