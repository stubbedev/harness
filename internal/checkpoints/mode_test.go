package checkpoints

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseMode(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]Mode{
		"":             ModeBoth,
		"both":         ModeBoth,
		"files":        ModeFiles,
		"conversation": ModeConversation,
	} {
		got, err := ParseMode(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, in := range []string{"bogus", "BOTH", " both", "file"} {
		_, err := ParseMode(in)
		require.Error(t, err, in)
	}
}

func TestModeUnmarshalText(t *testing.T) {
	t.Parallel()
	var m Mode
	require.NoError(t, m.UnmarshalText([]byte("files")))
	require.Equal(t, ModeFiles, m)
	require.NoError(t, m.UnmarshalText(nil))
	require.Equal(t, ModeBoth, m)
	require.Error(t, m.UnmarshalText([]byte("bogus")))
	require.Equal(t, ModeBoth, m)
}
