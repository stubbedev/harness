package common

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/home"
)

func TestShortPath(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	nested := filepath.Join(base, "internal", "ui", "chat", "toolgroup.go")

	require.Equal(t, filepath.Join("internal", "ui", "chat", "toolgroup.go"), ShortPath(base, nested),
		"a path inside the workspace reads relative to it")

	outside := filepath.Join(filepath.Dir(base), "elsewhere.go")
	assert.Equal(t, home.Short(outside), ShortPath(base, outside),
		"a path outside the workspace falls back to home-shortening")

	assert.Equal(t, "internal/ui/chat/toolgroup.go",
		ShortPath(base, "internal/ui/chat/toolgroup.go"),
		"an already-relative path is left alone")

	assert.Equal(t, home.Short(nested), ShortPath("", nested),
		"without a workspace only home-shortening applies")
}

func TestShortPathLeavesSiblingDirectoryAlone(t *testing.T) {
	t.Parallel()

	base := filepath.Join(t.TempDir(), "workspace")
	sibling := filepath.Join(filepath.Dir(base), "workspace-other", "main.go")

	got := ShortPath(base, sibling)
	assert.Equal(t, home.Short(sibling), got)
	assert.NotContains(t, got, ".."+string(filepath.Separator),
		"climbing out of the workspace must not render as a relative path")
}
