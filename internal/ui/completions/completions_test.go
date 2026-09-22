package completions

import (
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/ui/list"
)

// newMentionList builds the merged mention items and a filterable list
// holding them, the same way the mention picker dialog does.
func newMentionList(t *testing.T, files []FileCompletionValue, resources []ResourceCompletionValue, subagents []SubagentCompletionValue) (*list.FilterableList, []list.FilterableItem) {
	t.Helper()
	items := MentionItems(lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle(), files, resources, subagents)
	l := list.NewFilterableList()
	l.SetItems(items...)
	return l, items
}

func itemTexts(l *list.FilterableList) []string {
	texts := make([]string, 0, l.Len())
	for _, item := range l.FilteredItems() {
		if ci, ok := item.(*CompletionItem); ok {
			texts = append(texts, ci.Text())
		}
	}
	return texts
}

func TestFilterPrefersExactBasenameStem(t *testing.T) {
	t.Parallel()

	l, items := newMentionList(t, []FileCompletionValue{
		{Path: "internal/ui/chat/search.go"},
		{Path: "internal/ui/chat/user.go"},
	}, nil, nil)

	FilterMentionItems(l, items, "user")

	filtered := l.FilteredItems()
	require.NotEmpty(t, filtered)
	first, ok := filtered[0].(*CompletionItem)
	require.True(t, ok)
	require.Equal(t, "internal/ui/chat/user.go", first.Text())
	require.NotEmpty(t, first.match.MatchedIndexes)
}

func TestFilterPrefersBasenamePrefix(t *testing.T) {
	t.Parallel()

	l, items := newMentionList(t, []FileCompletionValue{
		{Path: "internal/ui/chat/mcp.go"},
		{Path: "internal/ui/model/chat.go"},
	}, nil, nil)

	FilterMentionItems(l, items, "chat.g")

	filtered := l.FilteredItems()
	require.NotEmpty(t, filtered)
	first, ok := filtered[0].(*CompletionItem)
	require.True(t, ok)
	require.Equal(t, "internal/ui/model/chat.go", first.Text())
	require.NotEmpty(t, first.match.MatchedIndexes)
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
			got := namePriorityTier(tt.path, tt.query)
			require.Equal(t, tt.wantTier, got)
		})
	}
}

func TestFilterPrefersPathSegmentExact(t *testing.T) {
	t.Parallel()

	l, items := newMentionList(t, []FileCompletionValue{
		{Path: "internal/ui/model/xychat.go"},
		{Path: "internal/ui/chat/mcp.go"},
	}, nil, nil)

	FilterMentionItems(l, items, "chat")

	filtered := l.FilteredItems()
	require.NotEmpty(t, filtered)
	first, ok := filtered[0].(*CompletionItem)
	require.True(t, ok)
	require.Equal(t, "internal/ui/chat/mcp.go", first.Text())
}

// TestMentionItems_SubagentsAppearsInList verifies that a subagent is
// represented in the merged list as an item whose text equals the
// subagent name.
func TestMentionItems_SubagentsAppearsInList(t *testing.T) {
	t.Parallel()

	l, _ := newMentionList(t, nil, nil, []SubagentCompletionValue{
		{Name: "code-reviewer", Description: "reviews code"},
	})

	require.Contains(t, itemTexts(l), "code-reviewer")
}

// TestMentionItems_SubagentAndFilesCoexist verifies that both file and
// subagent entries appear in the merged list.
func TestMentionItems_SubagentAndFilesCoexist(t *testing.T) {
	t.Parallel()

	l, _ := newMentionList(t,
		[]FileCompletionValue{{Path: "cmd/main.go"}},
		nil,
		[]SubagentCompletionValue{{Name: "tester", Description: "writes tests"}},
	)

	texts := itemTexts(l)
	require.Contains(t, texts, "cmd/main.go", "file item must appear in merged list")
	require.Contains(t, texts, "tester", "subagent item must appear in merged list")
}

// TestMentionItems_NilSubagents_NoError verifies that nil subagents
// does not panic and still populates file items normally.
func TestMentionItems_NilSubagents_NoError(t *testing.T) {
	t.Parallel()

	l, _ := newMentionList(t, []FileCompletionValue{{Path: "internal/foo.go"}}, nil, nil)

	require.Contains(t, itemTexts(l), "internal/foo.go")
}

// TestMentionItems_PreservesSubagentOrder verifies that multiple
// subagents appear in the merged list in the order they were passed,
// pinning the ordering contract so display matches input order.
func TestMentionItems_PreservesSubagentOrder(t *testing.T) {
	t.Parallel()

	l, _ := newMentionList(t, nil, nil, []SubagentCompletionValue{
		{Name: "zeta"},
		{Name: "alpha"},
		{Name: "mu"},
	})

	require.Equal(t, []string{"zeta", "alpha", "mu"}, itemTexts(l))
}
