package tools

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// resetSearchSpacing clears the package's search spacing state for one
// test and puts it back afterwards.
func resetSearchSpacing(t *testing.T) {
	t.Helper()
	lastSearchMu.Lock()
	savedLast, savedRecent, savedThrottled := lastSearchTime, recentSearches, throttledUntil
	lastSearchTime, recentSearches, throttledUntil = time.Time{}, nil, time.Time{}
	lastSearchMu.Unlock()
	t.Cleanup(func() {
		lastSearchMu.Lock()
		lastSearchTime, recentSearches, throttledUntil = savedLast, savedRecent, savedThrottled
		lastSearchMu.Unlock()
	})
}

// A burst of searches starts at once; the one past the burst keeps the
// gap, and a caller that gives up stops waiting at once.
func TestMaybeDelaySearch(t *testing.T) {
	resetSearchSpacing(t)

	start := time.Now()
	for range searchBurst {
		require.NoError(t, maybeDelaySearch(t.Context()))
	}
	require.Less(t, time.Since(start), 100*time.Millisecond, "a burst does not wait")

	require.NoError(t, maybeDelaySearch(t.Context()))
	require.GreaterOrEqual(t, time.Since(start), 500*time.Millisecond, "the search past the burst keeps the gap")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start = time.Now()
	require.ErrorIs(t, maybeDelaySearch(ctx), context.Canceled)
	require.Less(t, time.Since(start), 100*time.Millisecond, "a cancelled wait returns at once")
}

// After the backend throttled a search, even the next one keeps the gap.
func TestMaybeDelaySearchAfterThrottle(t *testing.T) {
	resetSearchSpacing(t)

	require.NoError(t, maybeDelaySearch(t.Context()))
	noteSearchThrottled()
	start := time.Now()
	require.NoError(t, maybeDelaySearch(t.Context()))
	require.GreaterOrEqual(t, time.Since(start), 400*time.Millisecond, "a throttled backend gets spaced searches")
}
