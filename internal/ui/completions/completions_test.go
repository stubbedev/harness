package completions

import (
	"testing"

	"github.com/sahilm/fuzzy"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/ui/list"
)

// mentionRow is a minimal filterable row the ranking tests filter with;
// the real rows are dialog.PickerItem values.
type mentionRow struct {
	*list.Versioned
	filter string
	match  fuzzy.Match
}

func (m *mentionRow) Render(int) string      { return "" }
func (m *mentionRow) ID() string             { return m.filter }
func (m *mentionRow) Filter() string         { return m.filter }
func (m *mentionRow) SetMatch(f fuzzy.Match) { m.match = f }
func (m *mentionRow) Finished() bool         { return true }

func rows(paths ...string) []list.FilterableItem {
	items := make([]list.FilterableItem, len(paths))
	for i, p := range paths {
		items[i] = &mentionRow{Versioned: list.NewVersioned(), filter: p}
	}
	return items
}

func rowTexts(l *list.FilterableList) []string {
	texts := make([]string, 0, l.Len())
	for _, item := range l.FilteredItems() {
		if r, ok := item.(*mentionRow); ok {
			texts = append(texts, r.filter)
		}
	}
	return texts
}

func TestFilterPrefersExactBasenameStem(t *testing.T) {
	t.Parallel()

	l := list.NewFilterableList()
	items := rows("internal/ui/chat/search.go", "internal/ui/chat/user.go")
	FilterMentionItems(l, items, "user")

	filtered := rowTexts(l)
	require.NotEmpty(t, filtered)
	require.Equal(t, "internal/ui/chat/user.go", filtered[0])
}

func TestFilterPrefersBasenamePrefix(t *testing.T) {
	t.Parallel()

	l := list.NewFilterableList()
	items := rows("internal/ui/chat/mcp.go", "internal/ui/model/chat.go")
	FilterMentionItems(l, items, "chat.g")

	filtered := rowTexts(l)
	require.NotEmpty(t, filtered)
	require.Equal(t, "internal/ui/model/chat.go", filtered[0])
}

func TestNamePriorityTier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		query    string
		wantTier int
	}{
		{
			name:     "exact stem",
			path:     "internal/ui/chat/user.go",
			query:    "user",
			wantTier: tierExactName,
		},
		{
			name:     "basename prefix",
			path:     "internal/ui/model/chat.go",
			query:    "chat.g",
			wantTier: tierPrefixName,
		},
		{
			name:     "path segment exact",
			path:     "internal/ui/chat/mcp.go",
			query:    "chat",
			wantTier: tierPathSegment,
		},
		{
			name:     "fallback",
			path:     "internal/ui/chat/search.go",
			query:    "user",
			wantTier: tierFallback,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.wantTier, namePriorityTier(tt.path, tt.query))
		})
	}
}

func TestFilterPrefersPathSegmentExact(t *testing.T) {
	t.Parallel()

	l := list.NewFilterableList()
	items := rows("internal/ui/model/xychat.go", "internal/ui/chat/mcp.go")
	FilterMentionItems(l, items, "chat")

	filtered := rowTexts(l)
	require.NotEmpty(t, filtered)
	require.Equal(t, "internal/ui/chat/mcp.go", filtered[0])
}
