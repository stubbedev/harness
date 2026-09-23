package dialog

import (
	"context"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
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
const RewindID ID = "rewind"

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
	// Without a snapshot there is nothing to restore on disk, so files
	// modes are not offered: the rewind dialog reports HasFiles from the
	// checkpoint rows, and the service degrades conversation-and-files
	// to conversation-only if the row vanished in between.
	if !hasFiles {
		return []rewindModeChoice{
			{mode: checkpoints.ModeConversation, label: "Conversation only"},
		}
	}
	return []rewindModeChoice{
		{mode: checkpoints.ModeBoth, label: "Conversation and files"},
		{mode: checkpoints.ModeConversation, label: "Conversation only"},
		{mode: checkpoints.ModeFiles, label: "Files only"},
	}
}

// Rewind is a picker over a session's user turns; selecting one offers
// what to restore: the transcript, the files on disk, or both.
type Rewind struct {
	com       *common.Common
	list      *list.FilterableList
	input     textinput.Model
	sessionID string
	turns     []RewindTurn
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

	r.input = textinput.New()
	r.input.SetVirtualCursor(false)
	r.input.Prompt = "❯ "
	r.input.Placeholder = "Filter turns"
	r.input.SetStyles(com.Styles.TextInput)
	r.input.Focus()

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
func (r *Rewind) ID() ID {
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
		default:
			// The typing path flows through the shared input helper and
			// the standard list filter.
			cmd, _ := filterInput(&r.input, msg, applyListFilter(r.list))
			return ActionCmd{cmd}
		}
	}
	return nil
}

// confirmSelection advances the phases: picking a turn opens the
// mode picker, picking a mode emits the confirmation. Both resolve
// through the selected row's value, never a positional index into
// the turns or choices: filtering reorders the list under the
// selection.
func (r *Rewind) confirmSelection() Action {
	item, ok := r.list.SelectedItem().(PickerItem)
	if !ok || item == nil {
		return nil
	}
	if r.phase == rewindPhaseTurns {
		if turn, ok := item.Value().(RewindTurn); ok {
			r.selected = turn
			r.enterModePhase()
		}
		return nil
	}
	choice, ok := item.Value().(rewindModeChoice)
	if !ok {
		return nil
	}
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
	choices := rewindModeChoices(r.selected.HasFiles)
	r.list.SetItems(rewindModeItems(r.com.Styles, choices)...)
	r.list.SetSelected(0)
	r.list.ScrollToTop()
}

// Draw implements Dialog.
func (r *Rewind) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	st := r.com.Styles
	width := DialogWidth(st, area)
	innerWidth := DialogInnerWidth(st, width)

	r.input.SetWidth(dialogInputTextWidth(st, r.input, innerWidth))
	listHeight, listTotalHeight, _ := sizeDialogList(st, r.list, innerWidth, rewindDialogMaxHeight, true)

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
	rc.AddInput(r.input.View())

	view := rc.Render()
	cur := DialogCursor(st, view, r.input.Cursor())
	DrawCenterCursor(scr, area, view, cur)
	return cur
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

// rewindTurnRightLabel is the right-hand info a turn row shows: when
// it was sent, and whether a working-tree snapshot backs it.
func rewindTurnRightLabel(turn RewindTurn) string {
	info := humanize.Time(turn.CreatedAt)
	if !turn.HasFiles {
		info += " · no files"
	}
	return info
}

// rewindTurnItems builds the turn rows through the shared picker item:
// the prompt is the label, the age and snapshot state sit on the right,
// and the raw prompt stays the filter text.
func rewindTurnItems(t *styles.Styles, turns []RewindTurn) []list.FilterableItem {
	items := make([]list.FilterableItem, len(turns))
	for i, turn := range turns {
		label := strings.ReplaceAll(turn.Prompt, "\n", " ")
		items[i] = NewPickerItem(t, turn, label, rewindTurnRightLabel(turn), turn.Prompt)
	}
	return items
}

// rewindModeRightLabel is the right-hand info a mode row shows: what
// the mode does to the transcript and the working tree.
func rewindModeRightLabel(mode checkpoints.Mode) string {
	switch mode {
	case checkpoints.ModeBoth:
		return "delete turns and restore files"
	case checkpoints.ModeConversation:
		return "delete turns only"
	case checkpoints.ModeFiles:
		return "restore files only"
	}
	return ""
}

// rewindModeItems builds the mode rows through the shared picker item;
// the choice struct is the value a selection resolves to.
func rewindModeItems(t *styles.Styles, choices []rewindModeChoice) []list.FilterableItem {
	items := make([]list.FilterableItem, len(choices))
	for i, choice := range choices {
		items[i] = NewPickerItem(t, choice, choice.label, rewindModeRightLabel(choice.mode))
	}
	return items
}
