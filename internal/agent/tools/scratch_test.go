package tools

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/require"
)

func TestWriteScratchFileWritesContent(t *testing.T) {
	root := scratchRootForTest(t)
	t.Setenv("HARNESS_SCRATCH_DIR", root)
	for _, kind := range []string{toolResultScratchKind, fetchScratchKind} {
		path, err := writeScratchFile("session", kind, "spill-*.txt", "payload")
		require.NoError(t, err)
		require.Equal(t, filepath.Join(root, "session", kind), filepath.Dir(path))
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, "payload", string(data))
	}
}

func TestWriteTempFileFailureLeavesNoFile(t *testing.T) {
	t.Parallel()
	failure := errors.New("disk gone")
	cases := map[string]struct {
		src  io.Reader
		size int64
		want error
	}{
		"read error":  {src: io.MultiReader(strings.NewReader("partial"), iotest.ErrReader(failure)), size: 64, want: failure},
		"short write": {src: strings.NewReader("partial"), size: 64, want: io.ErrShortWrite},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path, err := writeTempFile(dir, "page-*.md", tc.src, tc.size)
			require.ErrorIs(t, err, tc.want)
			require.Empty(t, path)
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}
