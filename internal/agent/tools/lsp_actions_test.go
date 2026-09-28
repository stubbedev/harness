package tools

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/memory"
)

// enumTag returns the enum tag values of the named field of T.
func enumTag[T any](t *testing.T, field string) []string {
	t.Helper()
	f, ok := reflect.TypeFor[T]().FieldByName(field)
	require.True(t, ok, field)
	return strings.Split(f.Tag.Get("enum"), ",")
}

// TestLSPActionEnumMatchesDispatch holds the schema the model sees to the
// actions the tool dispatches: an action missing from the enum can never
// be called, one missing from the dispatch is advertised but unknown.
func TestLSPActionEnumMatchesDispatch(t *testing.T) {
	t.Parallel()

	var dispatched []string
	for _, action := range slices.Sorted(maps.Keys(lspActions(nil, nil, nil))) {
		dispatched = append(dispatched, string(action))
	}
	enum := enumTag[LSPParams](t, "Action")
	slices.Sort(enum)
	require.Equal(t, dispatched, enum)
}

func TestMemoryCategoryEnumMatchesStore(t *testing.T) {
	t.Parallel()

	require.ElementsMatch(t, memory.ValidCategories(), enumTag[MemoryParams](t, "Category"))
}
