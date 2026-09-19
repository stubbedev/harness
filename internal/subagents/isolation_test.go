package subagents

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsolationFrontmatter(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "worktree", "none", "container", "WORKTREE"} {
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			s, err := ParseContent([]byte("---\nname: isolated\ndescription: Isolation test\nisolation: " + value + "\n---\nPrompt\n"))
			if value != "" && value != IsolationWorktree {
				if err == nil {
					err = s.Validate()
				}
				require.ErrorContains(t, err, "isolation")
				return
			}
			require.NoError(t, err)
			require.NoError(t, s.Validate())
			require.Equal(t, value, s.Isolation)
		})
	}
}
