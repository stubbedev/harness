package tools

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Searches in a burst still start at least the minimum gap apart, and a
// caller that gives up stops waiting at once.
func TestMaybeDelaySearch(t *testing.T) {
	lastSearchMu.Lock()
	saved := lastSearchTime
	lastSearchTime = time.Time{}
	lastSearchMu.Unlock()
	t.Cleanup(func() {
		lastSearchMu.Lock()
		lastSearchTime = saved
		lastSearchMu.Unlock()
	})

	start := time.Now()
	require.NoError(t, maybeDelaySearch(t.Context()))
	require.Less(t, time.Since(start), 100*time.Millisecond, "the first search in a while does not wait")

	require.NoError(t, maybeDelaySearch(t.Context()))
	require.GreaterOrEqual(t, time.Since(start), 500*time.Millisecond, "the next one keeps the gap")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start = time.Now()
	require.ErrorIs(t, maybeDelaySearch(ctx), context.Canceled)
	require.Less(t, time.Since(start), 100*time.Millisecond, "a cancelled wait returns at once")
}
