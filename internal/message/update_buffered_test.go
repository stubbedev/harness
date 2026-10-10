package message

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/db"
	"github.com/stubbedev/harness/internal/pubsub"
	"github.com/stubbedev/harness/internal/session"
)

// countingQuerier counts the message writes that reach SQL.
type countingQuerier struct {
	db.Querier
	updates atomic.Int64
}

func (c *countingQuerier) UpdateMessage(ctx context.Context, arg db.UpdateMessageParams) error {
	c.updates.Add(1)
	return c.Querier.UpdateMessage(ctx, arg)
}

func newCountingService(t *testing.T, opts ...ServiceOption) (Service, *countingQuerier, string) {
	t.Helper()
	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	q := db.New(conn)
	sess, err := session.NewService(q, conn).Create(t.Context(), "test")
	require.NoError(t, err)
	counting := &countingQuerier{Querier: q}
	return NewService(counting, opts...), counting, sess.ID
}

// toolCallBurst reports n finished tool calls one at a time, the way
// fantasy runs OnToolCall over a batch before dispatching it.
func toolCallBurst(t *testing.T, svc Service, msg *Message, n int) {
	t.Helper()
	for i := range n {
		msg.AddToolCall(ToolCall{ID: fmt.Sprintf("tc%d", i), Name: "view", Input: "{}", Finished: true})
		require.NoError(t, svc.UpdateBuffered(t.Context(), *msg))
	}
}

func TestUpdateBuffered_PublishesEachCallAndWritesOnce(t *testing.T) {
	t.Parallel()

	svc, counting, sessionID := newCountingService(t, WithDebounce(time.Hour))
	msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
	require.NoError(t, err)

	subCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	events := collect(subCtx, svc.Subscribe(subCtx))

	toolCallBurst(t, svc, &msg, 5)

	// Every call reached subscribers as it was reported, in order.
	require.Eventually(t, func() bool { return len(events.snapshot()) == 5 }, time.Second, 5*time.Millisecond)
	for i, ev := range events.snapshot() {
		require.Equal(t, pubsub.UpdatedEvent, ev.Type)
		require.Len(t, ev.Payload.ToolCalls(), i+1)
	}
	// None of them was written yet.
	require.Zero(t, counting.updates.Load())
	stored, err := svc.Get(t.Context(), msg.ID)
	require.NoError(t, err)
	require.Empty(t, stored.ToolCalls())

	// The first tool result writes the whole burst ahead of its own
	// row, once, and the write publishes nothing subscribers have not
	// already seen.
	_, err = svc.Create(t.Context(), sessionID, CreateMessageParams{
		Role:  Tool,
		Parts: []ContentPart{ToolResult{ToolCallID: "tc0", Name: "view", Content: "ok"}},
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), counting.updates.Load())
	stored, err = svc.Get(t.Context(), msg.ID)
	require.NoError(t, err)
	require.Len(t, stored.ToolCalls(), 5)
	for _, call := range stored.ToolCalls() {
		require.True(t, call.Finished)
	}

	// Later results find nothing left to write.
	_, err = svc.Create(t.Context(), sessionID, CreateMessageParams{
		Role:  Tool,
		Parts: []ContentPart{ToolResult{ToolCallID: "tc1", Name: "view", Content: "ok"}},
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), counting.updates.Load())

	require.Eventually(t, func() bool { return len(events.snapshot()) == 7 }, time.Second, 5*time.Millisecond)
	for _, ev := range events.snapshot()[5:] {
		require.Equal(t, pubsub.CreatedEvent, ev.Type, "the flush republished a state subscribers already had")
	}
}

func TestUpdateBuffered_ParallelResultsWriteOnceBeforeTheirRows(t *testing.T) {
	t.Parallel()

	svc, counting, sessionID := newCountingService(t, WithDebounce(time.Hour))
	msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
	require.NoError(t, err)
	toolCallBurst(t, svc, &msg, 20)

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			_, err := svc.Create(t.Context(), sessionID, CreateMessageParams{
				Role:  Tool,
				Parts: []ContentPart{ToolResult{ToolCallID: fmt.Sprintf("tc%d", i), Name: "view", Content: "ok"}},
			})
			require.NoError(t, err)
		})
	}
	wg.Wait()
	require.Equal(t, int64(1), counting.updates.Load())

	// The single write carried the whole batch.
	msgs, err := svc.List(t.Context(), sessionID)
	require.NoError(t, err)
	require.Len(t, msgs, 21)
	require.Len(t, msgs[0].ToolCalls(), 20)
}

func TestUpdateBuffered_WritesOnTheDebounceTick(t *testing.T) {
	t.Parallel()

	svc, counting, sessionID := newCountingService(t, WithDebounce(5*time.Millisecond))
	msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
	require.NoError(t, err)
	toolCallBurst(t, svc, &msg, 3)

	// With no result to force it, the burst still reaches SQL within a
	// debounce window, so a crash while the tools run keeps the calls.
	require.Eventually(t, func() bool {
		stored, err := svc.Get(t.Context(), msg.ID)
		return err == nil && len(stored.ToolCalls()) == 3
	}, time.Second, 5*time.Millisecond)
	require.Equal(t, int64(1), counting.updates.Load())
}

func TestUpdateBuffered_OtherSessionsDoNotFlush(t *testing.T) {
	t.Parallel()

	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	q := db.New(conn)
	sessions := session.NewService(q, conn)
	first, err := sessions.Create(t.Context(), "first")
	require.NoError(t, err)
	second, err := sessions.Create(t.Context(), "second")
	require.NoError(t, err)
	counting := &countingQuerier{Querier: q}
	svc := NewService(counting, WithDebounce(time.Hour))

	msg, err := svc.Create(t.Context(), first.ID, CreateMessageParams{Role: Assistant})
	require.NoError(t, err)
	toolCallBurst(t, svc, &msg, 2)

	_, err = svc.Create(t.Context(), second.ID, CreateMessageParams{Role: User, Parts: []ContentPart{TextContent{Text: "hi"}}})
	require.NoError(t, err)
	require.Zero(t, counting.updates.Load())

	require.NoError(t, svc.FlushAll(t.Context()))
	require.Equal(t, int64(1), counting.updates.Load())
}

// TestUpdate_StateAcceptedDuringTimerFlushIsWritten covers deltas that
// arrive while a timer-fired flush is writing. No timer can be armed
// during the write, so the flush must arm one on its way out; before,
// the deltas waited for whatever update came next, which a buffered
// burst ending the stream may never get.
func TestUpdate_StateAcceptedDuringTimerFlushIsWritten(t *testing.T) {
	t.Parallel()

	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	q := db.New(conn)
	sess, err := session.NewService(q, conn).Create(t.Context(), "test")
	require.NoError(t, err)
	slow := &slowUpdateQuerier{Querier: q, release: make(chan struct{}), started: make(chan struct{})}
	svc := NewService(slow, WithDebounce(5*time.Millisecond))

	msg, err := svc.Create(t.Context(), sess.ID, CreateMessageParams{Role: Assistant})
	require.NoError(t, err)
	msg.AppendContent("a")
	require.NoError(t, svc.Update(t.Context(), msg))
	<-slow.started

	// The timer-fired write of "a" is held; "b" arrives during it.
	msg.AppendContent("b")
	require.NoError(t, svc.Update(t.Context(), msg))
	close(slow.release)

	require.Eventually(t, func() bool {
		stored, err := svc.Get(t.Context(), msg.ID)
		return err == nil && stored.Content().Text == "ab"
	}, time.Second, 5*time.Millisecond)
}
