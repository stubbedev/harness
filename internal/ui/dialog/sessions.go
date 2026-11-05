package dialog

import (
	"fmt"
	"image"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/util"
)

// SessionsID is the identifier for the session selector dialog.
const SessionsID ID = "session"

const sessionDoubleClickThreshold = 400 * time.Millisecond

type sessionsMode uint8

// Possible modes a session item can be in
const (
	sessionsModeNormal sessionsMode = iota
	sessionsModeDeleting
	sessionsModeUpdating
)

// Session is a session selector dialog.
type Session struct {
	com      *common.Common
	list     *list.FilterableList
	input    textinput.Model
	sessions []session.Session
	// loading is set until the session list lands; loadErr says why it
	// did not. selectedSessionID is the session to select once it does.
	loading           bool
	loadErr           error
	selectedSessionID string

	sessionsMode        sessionsMode
	bodyArea            image.Rectangle
	scrolledToSelection bool
	lastClickTime       time.Time
	lastClickID         string

	keyMap struct {
		Select        key.Binding
		Next          key.Binding
		Previous      key.Binding
		UpDown        key.Binding
		Delete        key.Binding
		Rename        key.Binding
		ConfirmRename key.Binding
		CancelRename  key.Binding
		KeepTitle     key.Binding
		ConfirmDelete key.Binding
		CancelDelete  key.Binding
		Close         key.Binding
	}
}

var _ Dialog = (*Session)(nil)

// sessionsLoadedMsg carries the session list a Session dialog fetched.
// owner is the dialog that asked: a list for one since closed, or
// closed and reopened, is dropped.
type sessionsLoadedMsg struct {
	owner    *Session
	sessions []session.Session
	err      error
}

func (sessionsLoadedMsg) targetDialog() ID { return SessionsID }

// sessionDeleteCheckedMsg carries whether the session a delete was asked
// for is running, which refuses the delete.
type sessionDeleteCheckedMsg struct {
	owner     *Session
	sessionID string
	busy      bool
	err       error
}

func (sessionDeleteCheckedMsg) targetDialog() ID { return SessionsID }

// NewSessions creates a new Session dialog and the command that fetches
// its sessions; the list shows a loading state until they land.
func NewSessions(com *common.Common, selectedSessionID string) (*Session, tea.Cmd) {
	s := new(Session)
	s.sessionsMode = sessionsModeNormal
	s.com = com
	s.loading = true
	s.selectedSessionID = selectedSessionID

	s.list = list.NewFilterableList()
	s.list.Focus()

	s.input = textinput.New()
	s.input.SetVirtualCursor(false)
	s.input.Prompt = "❯ "
	s.input.Placeholder = "Enter session name"
	s.input.SetStyles(com.Styles.TextInput)
	s.input.Focus()

	km := dialogKeys()
	s.keyMap.Select = km.Sessions.Select
	s.keyMap.Next = km.Next
	s.keyMap.Previous = km.Previous
	s.keyMap.UpDown = km.UpDown
	s.keyMap.Delete = km.Sessions.Delete
	s.keyMap.Rename = km.Sessions.Rename
	s.keyMap.ConfirmRename = km.Sessions.ConfirmRename
	s.keyMap.CancelRename = km.Sessions.CancelRename
	s.keyMap.KeepTitle = km.Sessions.KeepTitle
	s.keyMap.ConfirmDelete = km.Sessions.ConfirmDelete
	s.keyMap.CancelDelete = km.Sessions.CancelDelete
	s.keyMap.Close = km.Close

	return s, s.loadSessionsCmd()
}

func (s *Session) loadSessionsCmd() tea.Cmd {
	ws := s.com.Workspace
	return func() tea.Msg {
		ctx, cancel := common.WorkspaceCallContext()
		defer cancel()
		sessions, err := ws.ListSessions(ctx)
		return sessionsLoadedMsg{owner: s, sessions: sessions, err: err}
	}
}

// applySessionsLoaded fills the list, selecting the session the dialog
// was opened on and applying whatever filter was typed meanwhile.
func (s *Session) applySessionsLoaded(msg sessionsLoadedMsg) Action {
	s.loading = false
	if msg.err != nil {
		s.loadErr = msg.err
		return ActionCmd{util.ReportError(fmt.Errorf("list sessions: %w", msg.err))}
	}
	s.sessions = msg.sessions
	s.list.SetItems(sessionItems(s.com.Styles, sessionsModeNormal, s.sessions...)...)
	if query := s.input.Value(); query != "" {
		applyListFilter(s.list)(query)
		return nil
	}
	for i, sess := range s.sessions {
		if sess.ID == s.selectedSessionID {
			s.list.SetSelected(i)
			break
		}
	}
	s.scrolledToSelection = false
	return nil
}

// checkDeleteCmd asks whether the session is running before the delete
// is offered: a running session cannot be deleted.
func (s *Session) checkDeleteCmd(sessionID string) tea.Cmd {
	ws := s.com.Workspace
	return func() tea.Msg {
		ctx, cancel := common.WorkspaceCallContext()
		defer cancel()
		msg := sessionDeleteCheckedMsg{owner: s, sessionID: sessionID}
		if ws.AgentIsReady(ctx) {
			msg.busy, msg.err = ws.AgentIsSessionBusy(ctx, sessionID)
		}
		return msg
	}
}

// applyDeleteChecked enters the delete confirmation, unless the session
// is running or the user moved on while the check was in flight.
func (s *Session) applyDeleteChecked(msg sessionDeleteCheckedMsg) Action {
	item := s.selectedSessionItem()
	if s.sessionsMode != sessionsModeNormal || item == nil || item.ID() != msg.sessionID {
		return nil
	}
	if msg.err != nil {
		return ActionCmd{util.ReportError(msg.err)}
	}
	if msg.busy {
		return ActionCmd{util.ReportAgentBusy("deleting a session")}
	}
	s.sessionsMode = sessionsModeDeleting
	s.list.SetItems(sessionItems(s.com.Styles, sessionsModeDeleting, s.sessions...)...)
	return nil
}

// ID implements Dialog.
func (s *Session) ID() ID {
	return SessionsID
}

// HandleMsg implements Dialog.
func (s *Session) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case sessionsLoadedMsg:
		if msg.owner != s {
			return nil
		}
		return s.applySessionsLoaded(msg)
	case sessionDeleteCheckedMsg:
		if msg.owner != s {
			return nil
		}
		return s.applyDeleteChecked(msg)
	case tea.KeyPressMsg:
		switch s.sessionsMode {
		case sessionsModeDeleting:
			switch {
			case key.Matches(msg, s.keyMap.ConfirmDelete):
				action := s.confirmDeleteSession()
				s.list.SetItems(sessionItems(s.com.Styles, sessionsModeNormal, s.sessions...)...)
				s.list.ScrollToSelected()
				return action
			case key.Matches(msg, s.keyMap.CancelDelete):
				s.sessionsMode = sessionsModeNormal
				s.list.SetItems(sessionItems(s.com.Styles, sessionsModeNormal, s.sessions...)...)
			}
		case sessionsModeUpdating:
			switch {
			case key.Matches(msg, s.keyMap.ConfirmRename):
				action := s.confirmRenameSession()
				s.list.SetItems(sessionItems(s.com.Styles, sessionsModeNormal, s.sessions...)...)
				return action
			case key.Matches(msg, s.keyMap.CancelRename):
				s.sessionsMode = sessionsModeNormal
				s.list.SetItems(sessionItems(s.com.Styles, sessionsModeNormal, s.sessions...)...)
			case key.Matches(msg, s.keyMap.KeepTitle) && s.canKeepTitle():
				s.selectedSessionItem().KeepTitle()
			default:
				return s.handleRenameInput(msg)
			}
		default:
			switch {
			case key.Matches(msg, s.keyMap.Close):
				return ActionClose{}
			case key.Matches(msg, s.keyMap.Rename):
				if s.selectedSessionItem() == nil {
					return nil
				}
				s.sessionsMode = sessionsModeUpdating
				s.list.SetItems(sessionItems(s.com.Styles, sessionsModeUpdating, s.sessions...)...)
			case key.Matches(msg, s.keyMap.Delete):
				if item := s.selectedSessionItem(); item != nil {
					return ActionCmd{s.checkDeleteCmd(item.ID())}
				}
			case key.Matches(msg, s.keyMap.Previous):
				s.list.Focus()
				selectPrevWrap(s.list)
			case key.Matches(msg, s.keyMap.Next):
				s.list.Focus()
				selectNextWrap(s.list)
			case key.Matches(msg, s.keyMap.Select):
				// The selection resolves through the selected item's value,
				// not its position: filtering reorders the list under the
				// selection.
				if item, ok := s.list.SelectedItem().(PickerItem); ok && item != nil {
					if sess, ok := item.Value().(session.Session); ok {
						return ActionSelectSession{sess}
					}
				}
			default:
				cmd, _ := filterInput(&s.input, msg, applyListFilter(s.list))
				return ActionCmd{cmd}
			}
		}
	case common.CoalescedWheelMsg:
		if image.Pt(msg.Mouse.X, msg.Mouse.Y).In(s.sessionListArea()) {
			s.list.ScrollBy(int(msg.DeltaY))
		}
	case tea.MouseClickMsg:
		return s.handleMouseClick(msg)
	default:
		if s.sessionsMode == sessionsModeUpdating {
			return s.handleRenameInput(msg)
		}
	}
	return nil
}

// handleRenameInput forwards msg to the rename input of the selected
// session. Pastes and the messages the input's own commands produce
// arrive here as well as keystrokes, so the input sees all of them.
func (s *Session) handleRenameInput(msg tea.Msg) Action {
	item := s.selectedSessionItem()
	if item == nil {
		return nil
	}
	return ActionCmd{item.HandleInput(msg)}
}

// canKeepTitle reports whether the keep-title binding currently applies
// to the selected session's rename input.
func (s *Session) canKeepTitle() bool {
	item := s.selectedSessionItem()
	return item != nil && item.CanKeepTitle()
}

func (s *Session) handleMouseClick(msg tea.MouseClickMsg) Action {
	if msg.Button != tea.MouseLeft || s.sessionsMode != sessionsModeNormal {
		s.resetMouseClick()
		return nil
	}
	area := s.sessionListArea()
	area.Max.X = min(area.Max.X, area.Min.X+s.list.Width())
	point := image.Pt(msg.X, msg.Y)
	if !point.In(area) {
		s.resetMouseClick()
		return nil
	}
	index, _ := s.list.ItemIndexAtPosition(point.X-area.Min.X, point.Y-area.Min.Y)
	if index < 0 {
		s.resetMouseClick()
		return nil
	}
	sessionItem := s.list.ItemAt(index).(*SessionItem)
	now := time.Now()
	if s.lastClickID == sessionItem.ID() && now.Sub(s.lastClickTime) <= sessionDoubleClickThreshold {
		s.resetMouseClick()
		return ActionSelectSession{sessionItem.Session}
	}
	s.lastClickTime = now
	s.lastClickID = sessionItem.ID()
	s.list.SetSelected(index)
	return nil
}

func (s *Session) resetMouseClick() {
	s.lastClickTime = time.Time{}
	s.lastClickID = ""
}

// Draw implements [Dialog].
func (s *Session) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := s.com.Styles
	s.bodyArea = image.Rectangle{}
	width := DialogWidth(t, area)
	height := DialogHeightCeiling(t, area, defaultDialogHeight)
	innerWidth := DialogInnerWidth(t, width)
	s.input.SetWidth(dialogInputTextWidth(t, s.input, innerWidth))
	listHeight, listTotalHeight, listWidth := sizeDialogList(t, s.list, innerWidth, height)

	// Hide the timestamps uniformly when the widest would crowd the title.
	applyInfoColumnVisibility(s.list.FilteredItems(), listWidth, sessionInfoMaxPercent)

	// Scroll the selected item into view once, on the first draw; after
	// that the view follows the user (wheel scrolls do not drag the
	// selection along) until a delete lands on an off-screen successor.
	if !s.scrolledToSelection {
		s.scrolledToSelection = true
		s.list.ScrollToSelected()
	}

	var cur *tea.Cursor
	rc := NewRenderContext(t, width)
	rc.Title = "Sessions"
	switch s.sessionsMode {
	case sessionsModeDeleting:
		rc.TitleStyle = t.Dialog.Sessions.DeletingTitle
		rc.TitleGradientFromColor = t.Dialog.Sessions.DeletingTitleGradientFromColor
		rc.TitleGradientToColor = t.Dialog.Sessions.DeletingTitleGradientToColor
		rc.ViewStyle = t.Dialog.Sessions.DeletingView
		rc.AddPart(t.Dialog.Sessions.DeletingMessage.Render("Delete this session?"))
	case sessionsModeUpdating:
		rc.TitleStyle = t.Dialog.Sessions.RenamingingTitle
		rc.TitleGradientFromColor = t.Dialog.Sessions.RenamingTitleGradientFromColor
		rc.TitleGradientToColor = t.Dialog.Sessions.RenamingTitleGradientToColor
		rc.ViewStyle = t.Dialog.Sessions.RenamingView
		message := t.Dialog.Sessions.RenamingingMessage.Render("Rename this session?")
		rc.AddPart(message)
		item := s.selectedSessionItem()
		if item == nil {
			return nil
		}
		cur = item.Cursor()
		if cur == nil {
			break
		}

		start, end := s.list.VisibleItemIndices()
		selectedIndex := s.list.Selected()

		// The input sits under the message, and the list body starts
		// at the first visible item, so walk down to the selected row.
		rows := lipgloss.Height(message) - 1
		for ; start <= end && start != selectedIndex && selectedIndex > -1; start++ {
			rows++
		}
		cur = inputCursorIn(cur, t.Dialog.Sessions.RenamingingTitle,
			t.Dialog.InputPrompt, t.Dialog.Sessions.RenamingView, rows)
	default:
		rc.AddInput(s.input.View())
		if status := s.loadStatus(); status != "" {
			rc.AddPart(t.Dialog.NormalItem.Render(status))
		}
	}
	bodyView := t.Dialog.List.Height(s.list.Height()).Render(s.list.Render())
	bodyView = joinScrollbar(t, bodyView, listHeight, listTotalHeight, listHeight, s.list.Offset())
	rc.AddPart(bodyView)

	view := rc.Render()
	if cur == nil && s.sessionsMode != sessionsModeUpdating {
		cur = DialogCursor(t, view, s.input.Cursor())
	}
	s.updateSessionListArea(area, view, bodyView, rc.ViewStyle, t.Dialog.List, innerWidth, listHeight)

	DrawCenterCursor(scr, area, view, cur)
	return cur
}

// loadStatus is what stands in for the list while it is not there: the
// fetch in flight, or the reason it failed.
func (s *Session) loadStatus() string {
	switch {
	case s.loading:
		return "Loading sessions…"
	case s.loadErr != nil:
		return "Could not load sessions."
	}
	return ""
}

func (s *Session) updateSessionListArea(
	area uv.Rectangle,
	view string,
	bodyView string,
	viewStyle lipgloss.Style,
	bodyStyle lipgloss.Style,
	bodyWidth int,
	bodyHeight int,
) {
	viewWidth, viewHeight := lipgloss.Size(view)
	dialogArea := common.CenterRect(area, min(viewWidth, area.Dx()), min(viewHeight, area.Dy()))
	bodyViewTop := dialogArea.Max.Y -
		viewStyle.GetMarginBottom() -
		viewStyle.GetBorderBottomSize() -
		viewStyle.GetPaddingBottom() -
		lipgloss.Height(bodyView)
	bodyMin := image.Pt(
		dialogArea.Min.X+
			viewStyle.GetMarginLeft()+
			viewStyle.GetBorderLeftSize()+
			viewStyle.GetPaddingLeft()+
			bodyStyle.GetMarginLeft()+
			bodyStyle.GetBorderLeftSize()+
			bodyStyle.GetPaddingLeft(),
		bodyViewTop+
			bodyStyle.GetMarginTop()+
			bodyStyle.GetBorderTopSize()+
			bodyStyle.GetPaddingTop(),
	)
	s.bodyArea = image.Rect(
		bodyMin.X,
		bodyMin.Y,
		bodyMin.X+bodyWidth,
		bodyMin.Y+bodyHeight,
	).Intersect(dialogArea).Intersect(area)
}

func (s *Session) sessionListArea() image.Rectangle {
	return s.bodyArea
}

func (s *Session) selectedSessionItem() *SessionItem {
	if item := s.list.SelectedItem(); item != nil {
		return item.(*SessionItem)
	}
	return nil
}

func (s *Session) confirmDeleteSession() Action {
	sessionItem := s.selectedSessionItem()
	s.sessionsMode = sessionsModeNormal
	if sessionItem == nil {
		return nil
	}

	s.removeSession(sessionItem.ID())
	return ActionCmd{s.deleteSessionCmd(sessionItem.ID())}
}

func (s *Session) removeSession(id string) {
	var newSessions []session.Session
	for _, sess := range s.sessions {
		if sess.ID == id {
			continue
		}
		newSessions = append(newSessions, sess)
	}
	s.sessions = newSessions
}

func (s *Session) deleteSessionCmd(id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := common.WorkspaceCallContext()
		defer cancel()
		err := s.com.Workspace.DeleteSession(ctx, id)
		if err != nil {
			return util.NewErrorMsg(err)
		}
		return nil
	}
}

func (s *Session) confirmRenameSession() Action {
	sessionItem := s.selectedSessionItem()
	s.sessionsMode = sessionsModeNormal
	if sessionItem == nil {
		return nil
	}

	newTitle := strings.TrimSpace(sessionItem.InputValue())
	if newTitle == "" || newTitle == sessionItem.Title {
		return nil
	}
	session := sessionItem.Session
	session.Title = newTitle
	s.updateSession(session)
	return ActionCmd{s.renameSessionCmd(session.ID, newTitle)}
}

func (s *Session) updateSession(session session.Session) {
	for existingID, sess := range s.sessions {
		if sess.ID == session.ID {
			s.sessions[existingID] = session
			break
		}
	}
}

func (s *Session) renameSessionCmd(id, title string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := common.WorkspaceCallContext()
		defer cancel()
		if err := s.com.Workspace.RenameSession(ctx, id, title); err != nil {
			return util.NewErrorMsg(err)
		}
		return nil
	}
}

// renameBindings returns the bindings routed in rename mode. The keep-title
// binding is listed only while it still applies.
func (s *Session) renameBindings() []key.Binding {
	bindings := []key.Binding{s.keyMap.CancelRename}
	if s.canKeepTitle() {
		bindings = append(bindings, s.keyMap.KeepTitle)
	}
	return append(bindings, s.keyMap.ConfirmRename)
}

// ShortHelp implements [help.KeyMap].
func (s *Session) ShortHelp() []key.Binding {
	switch s.sessionsMode {
	case sessionsModeDeleting:
		return []key.Binding{
			s.keyMap.ConfirmDelete,
			s.keyMap.CancelDelete,
		}
	case sessionsModeUpdating:
		return s.renameBindings()
	default:
		return []key.Binding{
			s.keyMap.UpDown,
			s.keyMap.Rename,
			s.keyMap.Delete,
			s.keyMap.Select,
			s.keyMap.Close,
		}
	}
}

// FullHelp implements [help.KeyMap].
func (s *Session) FullHelp() [][]key.Binding {
	m := [][]key.Binding{}
	slice := []key.Binding{
		s.keyMap.UpDown,
		s.keyMap.Rename,
		s.keyMap.Delete,
		s.keyMap.Select,
		s.keyMap.Close,
	}

	switch s.sessionsMode {
	case sessionsModeDeleting:
		slice = []key.Binding{
			s.keyMap.ConfirmDelete,
			s.keyMap.CancelDelete,
		}
	case sessionsModeUpdating:
		slice = s.renameBindings()
	default:
	}
	for i := 0; i < len(slice); i += 4 {
		end := min(i+4, len(slice))
		m = append(m, slice[i:end])
	}
	return m
}
