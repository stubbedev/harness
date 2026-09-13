package chat

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/stubbedev/harness/internal/ui/anim"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// SubagentWaitID is the stable ID of the transcript's wait entry.
const SubagentWaitID = "subagent-wait"

// SubagentWaitItem is the live transcript entry shown while subagents
// are running: the working spinner with an elapsed timer plus how many
// subagents the turn is waiting on. The UI appends it when dispatches
// appear and removes it when they all settle.
type SubagentWaitItem struct {
	*list.Versioned
	*cachedMessageItem
	*focusableMessageItem

	sty       *styles.Styles
	anim      *anim.Anim
	count     int
	startedAt time.Time
}

var (
	_ MessageItem = (*SubagentWaitItem)(nil)
	_ Animatable  = (*SubagentWaitItem)(nil)
)

// NewSubagentWaitItem creates the wait entry for count running
// subagents, the oldest started at startedAt.
func NewSubagentWaitItem(sty *styles.Styles, count int, startedAt time.Time) *SubagentWaitItem {
	v := list.NewVersioned()
	w := &SubagentWaitItem{
		Versioned:            v,
		cachedMessageItem:    &cachedMessageItem{},
		focusableMessageItem: newFocusableMessageItem(v),
		sty:                  sty,
		count:                count,
		startedAt:            startedAt,
	}
	w.anim = anim.New(anim.Settings{
		ID:         SubagentWaitID,
		Size:       15,
		GradColorA: sty.WorkingGradFromColor,
		GradColorB: sty.WorkingGradToColor,
		LabelColor: sty.WorkingLabelColor,
		Suffix: func() string {
			if w.startedAt.IsZero() {
				return ""
			}
			return common.FormatDuration(time.Since(w.startedAt))
		},
		SuffixColor: sty.WorkingTimerColor,
	})
	return w
}

// ID implements [Identifiable].
func (w *SubagentWaitItem) ID() string { return SubagentWaitID }

// Update refreshes the running count and the anchor time the elapsed
// timer counts from (the oldest running subagent).
func (w *SubagentWaitItem) Update(count int, startedAt time.Time) {
	changed := count != w.count || !startedAt.Equal(w.startedAt)
	w.count = count
	w.startedAt = startedAt
	if changed {
		w.clearCache()
		w.Bump()
	}
}

// Spinning implements [Animatable]: the entry animates while it exists.
func (w *SubagentWaitItem) Spinning() bool { return true }

// Advance implements [Animatable], bumping the version so the list cache
// re-renders the spinner frame.
func (w *SubagentWaitItem) Advance() bool {
	if !w.anim.Advance() {
		return false
	}
	w.Bump()
	return true
}

// Finished implements [list.Item]: never frozen while attached.
func (w *SubagentWaitItem) Finished() bool { return false }

// RawRender implements [MessageItem].
func (w *SubagentWaitItem) RawRender(width int) string {
	label := "subagent"
	if w.count != 1 {
		label = "subagents"
	}
	return w.anim.Render() + " " + w.sty.Tool.StateWaiting.Render(
		fmt.Sprintf("Waiting for %d %s...", w.count, label))
}

// Render renders the entry with the shared per-line focus prefix.
func (w *SubagentWaitItem) Render(width int) string {
	prefix := w.sty.Messages.ToolCallBlurred.Render()
	if w.focused {
		prefix = w.sty.Messages.ToolCallFocused.Render()
	}
	contentWidth := max(width-lipgloss.Width(prefix), 1)
	lines := strings.Split(w.RawRender(contentWidth), "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	return strings.Join(lines, "\n")
}
