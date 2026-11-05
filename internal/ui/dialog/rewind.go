package dialog

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustin/go-humanize"
	"github.com/stubbedev/harness/internal/checkpoints"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
	"github.com/stubbedev/harness/internal/ui/util"
	"github.com/stubbedev/harness/internal/workspace"
)

// RewindID is the identifier for the rewind (checkpoint) picker dialog.
const RewindID ID = "rewind"

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
	twoPhasePicker
	sessionID string
	turns     []RewindTurn
	selected  RewindTurn
	// loading is set until the turns land; loadErr says why they did
	// not.
	loading bool
	loadErr error
}

var _ Dialog = (*Rewind)(nil)

// rewindLoadedMsg carries the turns a Rewind dialog fetched. owner is
// the dialog that asked: turns for one since closed, or closed and
// reopened, are dropped.
type rewindLoadedMsg struct {
	owner *Rewind
	turns []RewindTurn
	err   error
}

func (rewindLoadedMsg) targetDialog() ID { return RewindID }

// NewRewind creates a rewind dialog for a session and the command that
// fetches its user turns; the list shows a loading state until they
// land.
func NewRewind(com *common.Common, sessionID string) (*Rewind, tea.Cmd) {
	km := dialogKeys()
	r := &Rewind{
		twoPhasePicker: newTwoPhasePicker(com, "Filter turns", km.Rewind.Select, km.Rewind.Back, km.Close),
		sessionID:      sessionID,
		loading:        true,
	}
	ws := com.Workspace
	return r, func() tea.Msg {
		ctx, cancel := common.WorkspaceCallContext()
		defer cancel()
		turns, err := fetchRewindTurns(ctx, ws, sessionID)
		return rewindLoadedMsg{owner: r, turns: turns, err: err}
	}
}

// fetchRewindTurns lists a session's user turns newest first, each
// marked with whether a working-tree snapshot backs it.
func fetchRewindTurns(ctx context.Context, ws workspace.Workspace, sessionID string) ([]RewindTurn, error) {
	messages, err := ws.ListUserMessages(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list the turns of session %s: %w", sessionID, err)
	}
	checkpointList, err := ws.ListCheckpoints(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list the checkpoints of session %s: %w", sessionID, err)
	}
	snapshotted := make(map[string]bool, len(checkpointList))
	for _, cp := range checkpointList {
		snapshotted[cp.MessageID] = true
	}

	// ListUserMessages is newest first already; keep only prompts with
	// text, so auxiliary user-role records (e.g. shell commands) do not
	// clutter the picker.
	var turns []RewindTurn
	for _, msg := range messages {
		prompt := userPromptText(msg)
		if prompt == "" {
			continue
		}
		turns = append(turns, RewindTurn{
			MessageID: msg.ID,
			Prompt:    prompt,
			CreatedAt: time.Unix(msg.CreatedAt, 0),
			HasFiles:  snapshotted[msg.ID],
		})
	}
	return turns, nil
}

// applyLoaded fills the turn list once the fetch lands.
func (r *Rewind) applyLoaded(msg rewindLoadedMsg) Action {
	r.loading = false
	if msg.err != nil {
		r.loadErr = msg.err
		return ActionCmd{util.ReportError(msg.err)}
	}
	r.turns = msg.turns
	if r.phase == pickerPhaseList {
		r.list.SetItems(rewindTurnItems(r.com.Styles, r.turns)...)
		if query := r.input.Value(); query != "" {
			applyListFilter(r.list)(query)
		}
	}
	return nil
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
	if loaded, ok := msg.(rewindLoadedMsg); ok {
		if loaded.owner != r {
			return nil
		}
		return r.applyLoaded(loaded)
	}
	return r.handleMsg(msg, r)
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
	if r.phase == pickerPhaseList {
		if turn, ok := item.Value().(RewindTurn); ok {
			r.selected = turn
			r.enterDetailPhase()
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

func (r *Rewind) enterListPhase() {
	r.phase = pickerPhaseList
	clearListFilter(&r.input, r.list)
	r.list.SetItems(rewindTurnItems(r.com.Styles, r.turns)...)
	r.list.SetSelected(0)
	r.list.ScrollToTop()
}

func (r *Rewind) enterDetailPhase() {
	r.phase = pickerPhaseDetail
	choices := rewindModeChoices(r.selected.HasFiles)
	clearListFilter(&r.input, r.list)
	r.list.SetItems(rewindModeItems(r.com.Styles, choices)...)
	r.list.SetSelected(0)
	r.list.ScrollToTop()
}

// Draw implements Dialog.
func (r *Rewind) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	st := r.com.Styles
	if r.phase == pickerPhaseList {
		header := ""
		switch {
		case r.loading:
			header = st.Dialog.NormalItem.Render("Loading turns…")
		case r.loadErr != nil:
			header = st.Dialog.NormalItem.Render("Could not load turns.")
		case len(r.turns) == 0:
			header = st.Dialog.NormalItem.Render("No user turns to rewind to yet.")
		}
		return r.draw(scr, area, "Rewind", header)
	}
	innerWidth := DialogInnerWidth(st, DialogWidth(st, area))
	prompt := ansi.Truncate(strings.ReplaceAll(r.selected.Prompt, "\n", " "), max(0, innerWidth-st.Dialog.NormalItem.GetHorizontalFrameSize()), "…")
	return r.draw(scr, area, "Rewind: restore what?", st.Dialog.NormalItem.Render(prompt))
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
