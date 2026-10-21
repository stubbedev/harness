package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func nestedIgnoreFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		".harnessignore":       "rootsecret.txt\n",
		".ignore":              "a.txt\n",
		"a.txt":                "needle\n",
		"rootsecret.txt":       "needle\n",
		"secret.txt":           "needle\n",
		"local.txt":            "needle\n",
		"sub/.harnessignore":   "secret.txt\n/local.txt\nprivate/\n",
		"sub/secret.txt":       "needle\n",
		"sub/local.txt":        "needle\n",
		"sub/keep.txt":         "needle\n",
		"sub/private/x.txt":    "needle\n",
		"sub/deeper/local.txt": "needle\n",
	}
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	return root
}

func matchedRelPaths(t *testing.T, root string, matches []grepMatch) []string {
	t.Helper()
	paths := make([]string, 0, len(matches))
	for _, m := range matches {
		rel, err := filepath.Rel(root, m.path)
		require.NoError(t, err)
		paths = append(paths, filepath.ToSlash(rel))
	}
	slices.Sort(paths)
	return paths
}

var nestedIgnoreWant = []string{"a.txt", "local.txt", "secret.txt", "sub/deeper/local.txt", "sub/keep.txt"}

func TestWalkerSearchRespectsNestedHarnessignore(t *testing.T) {
	t.Parallel()
	root := nestedIgnoreFixture(t)
	matches, err := searchFilesWithRegex("needle", root, "")
	require.NoError(t, err)
	require.Equal(t, nestedIgnoreWant, matchedRelPaths(t, root, matches))
}

func TestRipgrepSearchRespectsNestedHarnessignore(t *testing.T) {
	t.Parallel()
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep is not installed")
	}
	root := nestedIgnoreFixture(t)
	matches, err := searchWithRipgrep(t.Context(), rg, "needle", root, "")
	require.NoError(t, err)
	require.Equal(t, nestedIgnoreWant, matchedRelPaths(t, root, matches))
	for _, m := range matches {
		require.Equal(t, 1, m.lineNum)
		require.Equal(t, "needle", m.lineText)
	}
}

func TestRipgrepUnavailableIsReported(t *testing.T) {
	t.Parallel()
	_, err := searchWithRipgrep(t.Context(), "", "needle", t.TempDir(), "")
	require.ErrorIs(t, err, errRipgrepUnavailable)
}
