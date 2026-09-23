package discovery

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type def struct{ name, path string }

func defName(d def) string { return d.name }

func writeDef(t *testing.T, dir, file, name string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte(name), 0o644))
}

func loadDef(path string) (def, string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return def{}, "", err
	}
	name := strings.TrimSpace(string(content))
	if name == "bad" {
		return def{}, name, errors.New("invalid")
	}
	return def{name: name, path: path}, name, nil
}

func isDef(name string) bool { return strings.HasSuffix(name, ".def") }

// A later path must win a name collision whatever the paths' alphabetical
// order; sorting across bases would let /z lose to /a.
func TestWalkKeepsCallerPathPrecedence(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	late := filepath.Join(root, "a-project")
	early := filepath.Join(root, "z-global")
	writeDef(t, early, "x.def", "shared")
	writeDef(t, late, "x.def", "shared")

	items, states := Walk("def", []string{early, late}, isDef, loadDef)
	got := Dedupe(items, defName)
	require.Len(t, got, 1)
	require.Equal(t, filepath.Join(late, "x.def"), filepath.FromSlash(got[0].path))

	kept := DedupeStates(states)
	require.Len(t, kept, 1)
	require.Equal(t, filepath.Join(late, "x.def"), filepath.FromSlash(kept[0].Path))
}

func TestDedupeStatesKeepsEveryError(t *testing.T) {
	t.Parallel()

	states := []*State{
		{Name: "a", Path: "/1", State: StateError, Err: errors.New("x")},
		{Name: "a", Path: "/2", State: StateNormal},
		{Name: "a", Path: "/3", State: StateError, Err: errors.New("y")},
		{Name: "", Path: "/4", State: StateError, Err: errors.New("z")},
	}
	require.Len(t, DedupeStates(states), 4)
}

func TestWalkRecordsValidationFailureWithName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeDef(t, dir, "b.def", "bad")
	writeDef(t, dir, "ignored.txt", "nope")

	items, states := Walk("def", []string{dir}, isDef, loadDef)
	require.Empty(t, items)
	require.Len(t, states, 1)
	require.Equal(t, "bad", states[0].Name)
	require.Equal(t, StateError, states[0].State)
}

func TestFilter(t *testing.T) {
	t.Parallel()

	all := []def{{name: "a"}, {name: "b"}}
	require.Equal(t, []def{{name: "b"}}, Filter(all, []string{"a"}, defName))
	require.Equal(t, all, Filter(all, nil, defName))
}
