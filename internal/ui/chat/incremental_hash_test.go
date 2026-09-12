package chat

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIncrementalHashMatchesFullHash pins the only property that matters:
// whatever path sum takes, it must agree with hashing the string outright.
// A mismatch would key a render cache on a stale hash and freeze a section
// mid-stream.
func TestIncrementalHashMatchesFullHash(t *testing.T) {
	t.Parallel()

	var h incrementalHash
	var text strings.Builder

	// Append path, across the sample-length boundary where sum switches
	// from full re-hashes to continuing from saved state.
	for i := range 200 {
		text.WriteString("token ")
		if i%7 == 0 {
			text.WriteString("\n\nparagraph\n\n")
		}
		require.Equal(t, fnv64(text.String()), h.sum(text.String()),
			"append at step %d", i)
	}

	// Shrink: the saved length no longer applies.
	short := text.String()[:10]
	require.Equal(t, fnv64(short), h.sum(short))

	// Divergence: a retry rewrites the text from scratch. Same length as
	// what was hashed last, different bytes.
	diverged := strings.Repeat("x", len(short))
	require.Equal(t, fnv64(diverged), h.sum(diverged))

	// Empty, and growth again afterwards.
	require.Equal(t, fnv64(""), h.sum(""))
	require.Equal(t, fnv64("back again"), h.sum("back again"))
}

// TestIncrementalHashReset makes the reset path explicit: clearCache drops
// the saved state, and the next sum must still be correct.
func TestIncrementalHashReset(t *testing.T) {
	t.Parallel()

	var h incrementalHash
	long := strings.Repeat("streamed ", 50)
	require.Equal(t, fnv64(long), h.sum(long))

	h.reset()
	require.Zero(t, h.length)
	require.Equal(t, fnv64(long), h.sum(long))
}

// BenchmarkContentKeyStreaming approximates a streaming turn: the response
// grows a little between frames and every frame re-keys the content section.
// The incremental hash makes this linear in the response length rather than
// quadratic.
func BenchmarkContentKeyStreaming(b *testing.B) {
	var h incrementalHash
	var text strings.Builder
	for b.Loop() {
		text.WriteString("another chunk of streamed response text ")
		h.sum(text.String())
	}
}

// BenchmarkContentKeyStreamingFullHash is the same loop hashing from scratch
// each frame, which is what contentKey used to do.
func BenchmarkContentKeyStreamingFullHash(b *testing.B) {
	var text strings.Builder
	for b.Loop() {
		text.WriteString("another chunk of streamed response text ")
		fnv64(text.String())
	}
}
