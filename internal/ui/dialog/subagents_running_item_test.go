package dialog

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/subagents"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// TestNewRunningSubagentItemPickerRow pins the shared row surface: the
// running tab's row is a PickerItem whose value is the item data, whose
// label is the name, and whose right label carries the model, token
// count and status it showed on its single line.
func TestNewRunningSubagentItemPickerRow(t *testing.T) {
	t.Parallel()

	st := styles.CharmtonePantera()
	data := RunningSubagentItemData{
		ChildSessionID:   "c1",
		Name:             "researcher",
		Model:            "gpt-5",
		Status:           subagents.StatusRetrying,
		PromptTokens:     100,
		CompletionTokens: 50,
	}

	row := NewRunningSubagentItem(&st, data)
	require.Equal(t, "researcher", row.Label())
	require.Equal(t, data, row.Value().(RunningSubagentItemData))
	require.Contains(t, row.RightLabel(), "gpt-5")
	require.Contains(t, row.RightLabel(), "150 tok")
	require.Contains(t, row.RightLabel(), "(retrying)")
	require.Equal(t, "researcher gpt-5", row.Filter(), "the model stays part of the filter text")
}

// TestNewRunningSubagentItemLiveEntryDropsStatus pins the running case:
// a live entry shows no status suffix, and a zero-token entry no count.
func TestNewRunningSubagentItemLiveEntryDropsStatus(t *testing.T) {
	t.Parallel()

	st := styles.CharmtonePantera()
	row := NewRunningSubagentItem(&st, RunningSubagentItemData{
		Name:   "researcher",
		Model:  "gpt-5",
		Status: subagents.StatusRunning,
	})

	require.Equal(t, "gpt-5", row.RightLabel())
	require.Empty(t, row.Value().(RunningSubagentItemData).ChildSessionID)
}
