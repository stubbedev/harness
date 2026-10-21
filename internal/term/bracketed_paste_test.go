package term

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func referenceBracketedPaste(stream []byte) bool {
	on, off := bytes.LastIndex(stream, bracketedPasteOn), bytes.LastIndex(stream, bracketedPasteOff)
	return on > off
}

func feedChunks(stream []byte, sizes ...int) *Session {
	s := &Session{}
	for i := 0; len(stream) > 0; i++ {
		n := min(sizes[i%len(sizes)], len(stream))
		s.trackBracketedPasteLocked(stream[:n])
		stream = stream[n:]
	}
	return s
}

func TestTrackBracketedPasteFollowsTheLastSequenceAcrossEverySplit(t *testing.T) {
	t.Parallel()

	streams := [][]byte{
		[]byte("prompt \x1b[?2004h$ "),
		[]byte("\x1b[?2004hrunning\x1b[?2004l output"),
		[]byte("\x1b[?2004lx\x1b[?2004h"),
		[]byte("\x1b[?2004h\x1b[?2004l"),
		[]byte("\x1b[?2004l\x1b[?2004h"),
		[]byte("\x1b[?2004\x1b[?2004h"),
		[]byte("no sequence at all"),
	}
	for _, stream := range streams {
		want := referenceBracketedPaste(stream)
		for first := 1; first <= len(stream); first++ {
			for rest := 1; rest <= 9; rest++ {
				s := feedChunks(stream, first, rest)
				require.Equal(t, want, s.BracketedPaste(), "stream %q split %d then %d", stream, first, rest)
			}
		}
	}
}

func TestTrackBracketedPasteKeepsModeWithoutNewSequence(t *testing.T) {
	t.Parallel()

	s := &Session{}
	s.trackBracketedPasteLocked([]byte("\x1b[?2004h"))
	s.trackBracketedPasteLocked(bytes.Repeat([]byte("x"), 32*1024))
	require.True(t, s.BracketedPaste())
	s.trackBracketedPasteLocked([]byte("\x1b[?20"))
	s.trackBracketedPasteLocked([]byte("04l"))
	require.False(t, s.BracketedPaste())
}

func TestTrackBracketedPasteDoesNotAllocatePerChunk(t *testing.T) {
	s := &Session{}
	chunk := bytes.Repeat([]byte("output line\n"), 32*1024/12)
	s.trackBracketedPasteLocked(chunk)
	allocs := testing.AllocsPerRun(100, func() {
		s.trackBracketedPasteLocked(chunk)
	})
	require.Zero(t, allocs)
}

func BenchmarkTrackBracketedPaste(b *testing.B) {
	s := &Session{}
	chunk := bytes.Repeat([]byte("output line\n"), 32*1024/12)
	b.SetBytes(int64(len(chunk)))
	b.ReportAllocs()
	for b.Loop() {
		s.trackBracketedPasteLocked(chunk)
	}
}
