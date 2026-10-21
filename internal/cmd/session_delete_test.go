package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/session"
)

func TestConfirmSessionDeleteAcceptsYes(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	sess := session.Session{ID: "s1", Title: "Refactor parser"}
	require.True(t, confirmSessionDelete(strings.NewReader("y\n"), &out, sess))
	require.Contains(t, out.String(), "Refactor parser")
	require.Contains(t, out.String(), session.HashID(sess.ID)[:12])
}

func TestConfirmSessionDeleteDefaultsToNo(t *testing.T) {
	t.Parallel()

	sess := session.Session{ID: "s1", Title: "Refactor parser"}
	for _, input := range []string{"\n", "n\n", "nope\n", ""} {
		var out strings.Builder
		require.False(t, confirmSessionDelete(strings.NewReader(input), &out, sess), "input %q", input)
	}
}

func TestSessionDeleteCmd_HasForceFlag(t *testing.T) {
	t.Parallel()

	flag := sessionDeleteCmd.Flags().Lookup("force")
	require.NotNil(t, flag)
	require.Equal(t, "f", flag.Shorthand)
	require.Equal(t, "false", flag.DefValue)
}
