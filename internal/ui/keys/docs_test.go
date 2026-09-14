package keys

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// docsPath is the config reference that lists every rebindable action.
const docsPath = "../../../docs/config/README.md"

// TestDocsKeybindTableMatchesTheKeymap pins the documented action table to
// the keymap. The table is the only place a user finds out an action
// exists, so a binding added or rebound without touching the docs fails
// here with the rows to paste in.
func TestDocsKeybindTableMatchesTheKeymap(t *testing.T) {
	t.Parallel()

	bts, err := os.ReadFile(docsPath)
	require.NoError(t, err)

	var documented []string
	for line := range strings.Lines(string(bts)) {
		line = strings.TrimRight(line, "\n")
		// Table rows for actions, not the header or the separator.
		if strings.HasPrefix(line, "| `") && strings.HasSuffix(line, "` |") {
			documented = append(documented, line)
		}
	}

	var want []string
	for _, row := range ActionTable() {
		var quoted []string
		for k := range strings.SplitSeq(row[1], ", ") {
			quoted = append(quoted, "`"+k+"`")
		}
		want = append(want, fmt.Sprintf("| `%s` | %s |", row[0], strings.Join(quoted, ", ")))
	}

	require.Equal(t, want, documented,
		"the keybind table in %s is out of date; replace it with these rows", docsPath)
}
