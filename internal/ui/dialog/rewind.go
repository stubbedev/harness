package dialog

import (
	"context"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustin/go-humanize"
	"github.com/stubbedev/harness/internal/checkpoints"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/keys"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// RewindID is the identifier for the rewind (checkpoint) picker dialog.
const RewindID = "rewind"

const rewindDialogMaxHeight = 24

// RewindTurn is one rewound-to candidate: a user prompt, when it was
// sent, and whether a working-tree snapshot backs it.
type RewindTurn struct {
	MessageID string
	Prompt    string
	CreatedAt time.Time
	HasFiles  bool
}

// ActionRewindConfirmed is emitted once the user picked a turn and a
// rewind mode. The UI model performs the rewind.
type ActionRewindConfirmed struct {
	SessionID string
	MessageID string
	Prompt    string
	Mode      checkpoints.Mode
}

type rewindPhase uint8

const (
	rewindPhaseTurns rewindPhase = iota
	rewindPhaseMode
)

type rewindModeChoice struct {
	mode  checkpoints.Mode
	label string
}

func rewindModeChoices(hasFiles bool) []rewindModeChoice {
	choices := []rewindModeChoice{
		{mode: checkpoints.ModeBoth, label: "Conversation and files"},
		{mode: checkpoints.ModeConversation, label: "Conversation only"},
	}
	if hasFiles {
		choices = append(choices, rewindModeChoice{mode: checkpoints.ModeFiles, label: "Files only"})
	}
	return choices
}

// Rewind is a picker over a session's user turns; selecting one offers
// what to restore: the transcript, the files on disk, or both.
type Rewind struct {
	com       *common.Common
	list      *list.FilterableList
	sessionID string
	turns     []RewindTurn
	choices   []rewindModeChoice
	selected  RewindTurn
	phase     rewindPhase

	keyMap struct {
		Select   key.Binding
		Back     key.Binding
		Next     key.Binding
		Previous key.Binding
		UpDown   key.Binding
		Close    key.Binding
	}
}

var _ Dialog = (*Rewind)(nil)

// NewRewind creates a rewind dialog for a session, listing its user
// turns newest first.
func NewRewind(com *common.Common, sessionID string) (*Rewind, error) {
	r := &Rewind{com: com, sessionID: sessionID}

	messages, err := com.Workspace.ListUserMessages(context.TODO(), sessionID)
	if err != nil {
		return nil, err
	}
	checkpointList, err := com.Workspace.ListCheckpoints(context.TODO(), sessionID)
	if err != nil {
		return nil, err
	}
	snapshotted := make(map[string]bool, len(checkpointList))
	for _, cp := range checkpointList {
		snapshotted[cp.MessageID] = true
	}

	// ListUserMessages is newest first already; keep only prompts with
	// text, so auxiliary user-role records (e.g. shell commands) do not
	// clutter the picker.
	for _, msg := range messages {
		prompt := userPromptText(msg)
		if prompt == "" {
			continue
		}
		r.turns = append(r.turns, RewindTurn{
			MessageID: msg.ID,
			Prompt:    prompt,
			CreatedAt: time.Unix(msg.CreatedAt, 0),
			HasFiles:  snapshotted[msg.ID],
		})
	}

	r.list = list.NewFilterableList(rewindTurnItems(com.Styles, r.turns)...)
	r.list.Focus()

	km := dialogKeys()
	r.keyMap.Select = km.Rewind.Select
	r.keyMap.Back = km.Rewind.Back
	r.keyMap.Next = keys.WithDesc(km.Next, "next")
	r.keyMap.Previous = keys.WithDesc(km.Previous, "previous")
	r.keyMap.UpDown = km.UpDown
	r.keyMap.Close = km.Close

	return r, nil
}

// userPromptText returns the first non-empty text part of a user
// message.
func userPromptText(msg message.Message) string {
	text, ok := message.PartOf[message.TextContent](&msg)
	if !ok || strings.TrimSpace(text.Text) == "" {
		return ""
	}
	return text.Text
}

// ID implements Dialog.
func (r *Rewind) ID() string {
	return RewindID
}

// HandleMsg implements Dialog.
func (r *Rewind) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, r.keyMap.Close):
			if r.phase == rewindPhaseMode {
				r.enterTurnsPhase()
				return nil
			}
			return ActionClose{}
		case key.Matches(msg, r.keyMap.Back) && r.phase == rewindPhaseMode:
			r.enterTurnsPhase()
			return nil
		case key.Matches(msg, r.keyMap.Previous):
			if r.list.IsSelectedFirst() {
				r.list.SelectLast()
			} else {
				r.list.SelectPrev()
			}
			r.list.ScrollToSelected()
		case key.Matches(msg, r.keyMap.Next):
			if r.list.IsSelectedLast() {
				r.list.SelectFirst()
			} else {
				r.list.SelectNext()
			}
			r.list.ScrollToSelected()
		case key.Matches(msg, r.keyMap.Select):
			return r.confirmSelection()
		}
	}
	return nil
}

func (r *Rewind) confirmSelection() Action {
	idx := r.list.Selected()
	if r.phase == rewindPhaseTurns {
		if idx < 0 || idx >= len(r.turns) {
			return nil
		}
		r.selected = r.turns[idx]
		r.enterModePhase()
		return nil
	}
	if idx < 0 || idx >= len(r.choices) {
		return nil
	}
	choice := r.choices[idx]
	return ActionRewindConfirmed{
		SessionID: r.sessionID,
		MessageID: r.selected.MessageID,
		Prompt:    r.selected.Prompt,
		Mode:      choice.mode,
	}
}

func (r *Rewind) enterTurnsPhase() {
	r.phase = rewindPhaseTurns
	r.list.SetItems(rewindTurnItems(r.com.Styles, r.turns)...)
	r.list.SetSelected(0)
	r.list.ScrollToTop()
}

func (r *Rewind) enterModePhase() {
	r.phase = rewindPhaseMode
	r.choices = rewindModeChoices(r.selected.HasFiles)
	items := make([]list.FilterableItem, len(r.choices))
	for i, choice := range r.choices {
		items[i] = newRewindModeItem(r.com.Styles, choice)
	}
	r.list.SetItems(items...)
	r.list.SetSelected(0)
	r.list.ScrollToTop()
}

// Draw implements Dialog.
func (r *Rewind) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	st := r.com.Styles
	width := DialogWidth(st, area)
	innerWidth := DialogInnerWidth(st, width)

	listHeight, listTotalHeight, _ := sizeDialogList(st, r.list, innerWidth, rewindDialogMaxHeight)

	rc := NewRenderContext(st, width)
	if r.phase == rewindPhaseTurns {
		rc.Title = "Rewind"
		if len(r.turns) == 0 {
			rc.AddPart(st.Dialog.NormalItem.Render("No user turns to rewind to yet."))
		}
	} else {
		rc.Title = "Rewind: restore what?"
		prompt := ansi.Truncate(strings.ReplaceAll(r.selected.Prompt, "\n", " "), max(0, innerWidth-st.Dialog.NormalItem.GetHorizontalFrameSize()), "…")
		rc.AddPart(st.Dialog.NormalItem.Render(prompt))
	}

	r.list.ScrollToSelected()
	listView := st.Dialog.List.Height(r.list.Height()).Render(r.list.Render())
	listView = joinScrollbar(st, listView, listHeight, listTotalHeight, listHeight, r.list.Offset())
	rc.AddPart(listView)

	view := rc.Render()
	DrawCenterCursor(scr, area, view, nil)
	return nil
}

// ShortHelp implements help.KeyMap.
func (r *Rewind) ShortHelp() []key.Binding {
	return []key.Binding{
		r.keyMap.UpDown,
		r.keyMap.Select,
		r.keyMap.Back,
		r.keyMap.Close,
	}
}

// FullHelp implements help.KeyMap.
func (r *Rewind) FullHelp() [][]key.Binding {
	return [][]key.Binding{r.ShortHelp()}
}

// -- items --

// rewindTurnItem renders one user turn row.
type rewindTurnItem struct {
	*list.Versioned
	turn    RewindTurn
	t       *styles.Styles
	focused bool
	cache   map[int]string
}

func rewindTurnItems(t *styles.Styles, turns []RewindTurn) []list.FilterableItem {
	items := make([]list.FilterableItem, len(turns))
	for i, turn := range turns {
		items[i] = &rewindTurnItem{
			Versioned: list.NewVersioned(),
			turn:      turn,
			t:         t,
		}
	}
	return items
}

var (
	_ list.FilterableItem = (*rewindTurnItem)(nil)
	_ list.Focusable      = (*rewindTurnItem)(nil)
)

// Filter implements list.FilterableItem.
func (i *rewindTurnItem) Filter() string {
	return i.turn.Prompt
}

// SetFocused implements list.Focusable.
func (i *rewindTurnItem) SetFocused(focused bool) {
	if i.focused == focused {
		return
	}
	i.cache = nil
	i.focused = focused
	i.Bump()
}

// Finished implements list.Item.
func (i *rewindTurnItem) Finished() bool {
	return true
}

// Render implements list.Item.
func (i *rewindTurnItem) Render(width int) string {
	info := humanize.Time(i.turn.CreatedAt)
	if !i.turn.HasFiles {
		info += " · no files"
	}
	return renderItem(
		ListItemStyles{
			ItemBlurred:     i.t.Dialog.NormalItem,
			ItemFocused:     i.t.Dialog.SelectedItem,
			InfoTextBlurred: i.t.Dialog.ListItem.InfoBlurred,
			InfoTextFocused: i.t.Dialog.ListItem.InfoFocused,
		},
		strings.ReplaceAll(i.turn.Prompt, "\n", " "),
		info,
		i.focused,
		width,
		i.cache,
		nil,
	)
}

// rewindModeItem renders one rewind mode row.
type rewindModeItem struct {
	*list.Versioned
	choice  rewindModeChoice
	t       *styles.Styles
	focused bool
	cache   map[int]string
}

func newRewindModeItem(t *styles.Styles, choice rewindModeChoice) *rewindModeItem {
	return &rewindModeItem{Versioned: list.NewVersioned(), choice: choice, t: t}
}

var (
	_ list.FilterableItem = (*rewindModeItem)(nil)
	_ list.Focusable      = (*rewindModeItem)(nil)
)

// Filter implements list.FilterableItem.
func (i *rewindModeItem) Filter() string {
	return i.choice.label
}

// SetFocused implements list.Focusable.
func (i *rewindModeItem) SetFocused(focused bool) {
	if i.focused == focused {
		return
	}
	i.cache = nil
	i.focused = focused
	i.Bump()
}

// Finished implements list.Item.
func (i *rewindModeItem) Finished() bool {
	return true
}

// Render implements list.Item.
func (i *rewindModeItem) Render(width int) string {
	info := ""
	switch i.choice.mode {
	case checkpoints.ModeBoth:
		info = "delete turns and restore files"
	case checkpoints.ModeConversation:
		info = "delete turns only"
	case checkpoints.ModeFiles:
		info = "restore files only"
	}
	return renderItem(
		ListItemStyles{
			ItemBlurred:     i.t.Dialog.NormalItem,
			ItemFocused:     i.t.Dialog.SelectedItem,
			InfoTextBlurred: i.t.Dialog.ListItem.InfoBlurred,
			InfoTextFocused: i.t.Dialog.ListItem.InfoFocused,
		},
		i.choice.label,
		info,
		i.focused,
		width,
		i.cache,
		nil,
	)
}
