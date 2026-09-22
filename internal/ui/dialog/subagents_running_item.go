package dialog

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// RunningSubagentItemData holds the data for a running subagent list item.
type RunningSubagentItemData struct {
	ChildSessionID   string
	Name             string
	Color            string
	Model            string
	Status           string
	PromptTokens     int64
	CompletionTokens int64
}

// NewRunningSubagentItem creates the running tab's picker row through the
// shared picker item: the subagent's name is the label, and the model,
// token count and status it showed on its single line become the
// right-hand info. The data is the value a selection resolves to.
func NewRunningSubagentItem(t *styles.Styles, data RunningSubagentItemData) PickerItem {
	var info []string
	if data.Model != "" {
		info = append(info, data.Model)
	}
	if count := common.FormatSubagentTokenCount(data.PromptTokens, data.CompletionTokens); count != "" {
		info = append(info, count)
	}
	// A live entry is "running"; anything else (retrying while
	// credentials refresh) is worth spelling out. The shared suffix is
	// styled for inline use; the picker row owns its right column's
	// styling, so only the text carries over.
	if suffix := ansi.Strip(common.SubagentStatusSuffix(t, data.Status)); suffix != "" {
		info = append(info, suffix)
	}
	return NewPickerItem(t, data, data.Name, strings.Join(info, "  "), data.Name+" "+data.Model)
}
