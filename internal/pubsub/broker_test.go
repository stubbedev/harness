package pubsub

import (
	"sync"
	"testing"

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
