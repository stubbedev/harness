package pubsub

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestShutdownConcurrent: racing Shutdown calls must not close done (or
// a subscriber channel) twice.
func TestShutdownConcurrent(t *testing.T) {
	t.Parallel()

	b := NewBroker[int]()
	sub := b.Subscribe(t.Context())

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(b.Shutdown)
	}
	wg.Wait()

	_, ok := <-sub
	require.False(t, ok, "subscriber channel closed on shutdown")
	require.Zero(t, b.GetSubscriberCount())
}

// TestPublishMustDeliverMarksTheEvent pins the flag a fan-in reads to
// forward a terminal event with the same guarantee, and that a canceled
// context still gives every subscriber the non-blocking attempt.
func TestPublishMustDeliverMarksTheEvent(t *testing.T) {
	t.Parallel()

	b := NewBroker[int]()
	t.Cleanup(b.Shutdown)
	first := b.Subscribe(t.Context())
	second := b.Subscribe(t.Context())

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	b.PublishMustDeliver(ctx, UpdatedEvent, 7)
	for _, ch := range []<-chan Event[int]{first, second} {
		ev := <-ch
		require.True(t, ev.MustDeliver)
		require.Equal(t, 7, ev.Payload)
	}

	b.Publish(UpdatedEvent, 8)
	require.False(t, (<-first).MustDeliver)
}

// TestEvictingBrokerDisconnectsAFullSubscriber: a must-deliver event
// that finds a subscriber's buffer full closes that subscriber at once
// instead of waiting on it, and leaves the others subscribed.
func TestEvictingBrokerDisconnectsAFullSubscriber(t *testing.T) {
	t.Parallel()

	b := NewEvictingStreamingBroker[int]()
	b.SetMustDeliverTimeout(time.Hour)
	stalledCtx, cancelStalled := context.WithCancel(t.Context())
	stalled := b.Subscribe(stalledCtx)
	healthy := b.Subscribe(t.Context())

	for i := range streamingBufferSize {
		b.Publish(UpdatedEvent, i)
		<-healthy
	}
	done := make(chan struct{})
	go func() {
		b.PublishMustDeliver(t.Context(), UpdatedEvent, -1)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("PublishMustDeliver waited on the full subscriber")
	}

	require.Equal(t, -1, (<-healthy).Payload)
	require.Equal(t, 1, b.GetSubscriberCount())
	drained := 0
	for range stalled {
		drained++
	}
	require.Equal(t, streamingBufferSize, drained)

	cancelStalled()
	require.Eventually(t, func() bool { return b.GetSubscriberCount() == 1 }, time.Second, time.Millisecond)
	b.Shutdown()
}
