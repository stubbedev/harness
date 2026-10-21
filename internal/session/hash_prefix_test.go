package session

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFilterHashPrefixMatchesPrefix(t *testing.T) {
	t.Parallel()

	ids := []string{"a", "b"}
	identity := func(s string) string { return s }

	require.Equal(t, []string{"a"}, FilterHashPrefix(ids, HashID("a")[:6], identity))
	require.Equal(t, []string{"b"}, FilterHashPrefix(ids, HashID("b"), identity))
}

func TestFilterHashPrefixRejectsEmptyID(t *testing.T) {
	t.Parallel()

	ids := []string{"only"}
	require.Empty(t, FilterHashPrefix(ids, "", func(s string) string { return s }))
}
