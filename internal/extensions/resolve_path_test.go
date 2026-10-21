package extensions

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolvePathJoinsRelativePathsOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	in := &instance{host: &Host{opts: Options{WorkingDir: root}}}
	require.Equal(t, filepath.Join(root, "sub", "a.lua"), in.resolvePath(filepath.Join("sub", "..", "sub", "a.lua")))
	abs := filepath.Join(t.TempDir(), "a.lua")
	require.Equal(t, abs, in.resolvePath(abs+string(filepath.Separator)))
	if runtime.GOOS == "windows" {
		require.Equal(t, filepath.Clean("/elsewhere/a.lua"), in.resolvePath("/elsewhere/a.lua"))
	}
}
