package filetracker

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvidenceVersionsAndRanges(t *testing.T) {
	t.Parallel()
	tracker := NewService(nil).(Evidence)
	ctx := t.Context()
	before := []byte("alpha\nbeta\ngamma\n")
	tracker.Observe(ctx, "session", "file", before, []Range{{0, 6}})
	require.NoError(t, tracker.Check(ctx, "session", "file", before, []Range{{0, 5}}))
	require.ErrorIs(t, tracker.Check(ctx, "session", "file", before, []Range{{6, 10}}), ErrUnread)
	require.ErrorIs(t, tracker.Check(ctx, "other", "file", before, nil), ErrUnread)
	changed := []byte("ALPHA\nbeta\ngamma\n")
	require.ErrorIs(t, tracker.Check(ctx, "session", "file", changed, nil), ErrStale)
	tracker.Observe(ctx, "session", "file", changed, []Range{{6, 11}})
	require.ErrorIs(t, tracker.Check(ctx, "session", "file", changed, []Range{{0, 5}}), ErrUnread)
	tracker.Observe(ctx, "session", "file", changed, []Range{{0, 6}, {11, len(changed)}})
	require.NoError(t, tracker.Check(ctx, "session", "file", changed, []Range{{0, len(changed)}}))
}

func TestEvidenceAdvancePreservesOnlyObservedAndCreatedBytes(t *testing.T) {
	t.Parallel()
	tracker := NewService(nil).(Evidence)
	ctx := t.Context()
	before := []byte("alpha\nbeta\ngamma\n")
	after := []byte("longer alpha\nbeta\nGAMMA\n")
	tracker.Observe(ctx, "s", "file", before, []Range{{0, 6}, {11, len(before)}})
	tracker.Advance(ctx, "s", "file", before, after)
	require.NoError(t, tracker.Check(ctx, "s", "file", after, []Range{{0, 13}, {18, len(after)}}))
	require.ErrorIs(t, tracker.Check(ctx, "s", "file", after, []Range{{13, 17}}), ErrUnread)
	require.ErrorIs(t, tracker.Check(ctx, "s", "file", after, []Range{{0, len(after)}}), ErrUnread)
}

func TestEvidenceConcurrentObservations(t *testing.T) {
	t.Parallel()
	tracker := NewService(nil).(Evidence)
	data := make([]byte, 100)
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Go(func() { tracker.Observe(t.Context(), "s", "file", data, []Range{{i, i + 1}}) })
	}
	wg.Wait()
	require.NoError(t, tracker.Check(t.Context(), "s", "file", data, []Range{{0, len(data)}}))
}
