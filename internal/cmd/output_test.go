package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfirmPrompt(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		"y\n":     true,
		"Y\n":     true,
		"yes\n":   true,
		"YES":     true,
		" yes \n": true,
		"n\n":     false,
		"\n":      false,
		"":        false,
		"maybe\n": false,
	}
	for input, want := range cases {
		var out strings.Builder
		require.Equal(t, want, confirmPrompt(strings.NewReader(input), &out, "ok? "), "input %q", input)
		require.Equal(t, "ok? ", out.String())
	}
}
