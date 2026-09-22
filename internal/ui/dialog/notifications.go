package dialog

import (
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/notification"
)

const (
	// NotificationsID is the identifier for the notification style picker dialog.
	NotificationsID              = "notifications"
	notificationsDialogMaxHeight = 12
)

// NotificationStyle represents a notification backend option.
type NotificationStyle struct {
	ID          string
	Title       string
	Description string
}

// AllNotificationStyles lists all available notification styles in order.
var AllNotificationStyles = []NotificationStyle{
	{ID: "auto", Title: "Auto", Description: "Automatically detect the best backend"},
	{ID: "native", Title: "Native", Description: "Use system notifications (macOS/Linux/Windows)"},
	{ID: "osc", Title: "OSC", Description: "Use terminal OSC escape sequences"},
	{ID: "bell", Title: "Bell", Description: "Use terminal bell character"},
	{ID: "disabled", Title: "Disabled", Description: "Turn off notifications"},
}

// Notifications represents a dialog for selecting notification style.
type Notifications struct {
	com   *common.Common
	list  *list.FilterableList
	input textinput.Model

	keyMap struct {
		Select   key.Binding
		Next     key.Binding
		Previous key.Binding
		UpDown   key.Binding
		Close    key.Binding
	}
}

var _ Dialog = (*Notifications)(nil)

// NewNotifications creates a new notification style picker dialog.
func NewNotifications(com *common.Common) *Notifications {
	n := &Notifications{com: com}

	n.list = list.NewFilterableList()
	n.list.Focus()

	n.input = textinput.New()
	n.input.SetVirtualCursor(false)
	n.input.Prompt = "❯ "
	n.input.Placeholder = "Type to filter"
	n.input.SetStyles(com.Styles.TextInput)
	n.input.Focus()

	km := dialogKeys()
	n.keyMap.Select = km.Select
	n.keyMap.Next = km.Next
	n.keyMap.Previous = km.Previous
	n.keyMap.UpDown = km.UpDown
	n.keyMap.Close = km.Close

	n.setItems()
	return n
}

// ID implements Dialog.
func (n *Notifications) ID() string {
	return NotificationsID
}

// HandleMsg implements [Dialog].
func (n *Notifications) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, n.keyMap.Close):
			return ActionClose{}
		case key.Matches(msg, n.keyMap.Previous):
			n.list.Focus()
			if n.list.IsSelectedFirst() {
				n.list.SelectLast()
				n.list.ScrollToBottom()
				break
			}
			n.list.SelectPrev()
			n.list.ScrollToSelected()
		case key.Matches(msg, n.keyMap.Next):
			n.list.Focus()
			if n.list.IsSelectedLast() {
				n.list.SelectFirst()
				n.list.ScrollToTop()
				break
			}
			n.list.SelectNext()
			n.list.ScrollToSelected()
		case key.Matches(msg, n.keyMap.Select):
			if item, ok := n.list.SelectedItem().(PickerItem); ok && item != nil {
				if style, ok := item.Value().(string); ok {
					return ActionSelectNotificationStyle{Style: style}
				}
			}
		default:
			cmd, _ := filterInput(&n.input, msg, applyListFilter(n.list))
			return ActionCmd{cmd}
		}
	}
	return nil
}

// Cursor returns the cursor position relative to the dialog.
func (n *Notifications) Cursor() *tea.Cursor {
	return InputCursor(n.com.Styles, n.input.Cursor())
}

// Draw implements [Dialog].
func (n *Notifications) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := n.com.Styles
	width := DialogWidth(t, area)
	height := DialogHeightCeiling(t, area, notificationsDialogMaxHeight)
	innerWidth := DialogInnerWidth(t, width)
	heightOffset := dialogChromeHeight(t, t.Dialog.HelpView)

	n.input.SetWidth(dialogInputTextWidth(t, n.input, innerWidth))
	n.list.SetSize(innerWidth, max(0, height-heightOffset))

	rc := NewRenderContext(t, width)
	rc.Title = "Notification Style"
	rc.AddInput(n.input.View())

	visibleCount := len(n.list.FilteredItems())
	if n.list.Height() >= visibleCount {
		n.list.ScrollToTop()
	} else {
		n.list.ScrollToSelected()
	}

	listView := t.Dialog.List.Height(n.list.Height()).Render(n.list.Render())
	rc.AddPart(listView)

	view := rc.Render()

	cur := DialogCursor(t, view, n.input.Cursor())
	DrawCenterCursor(scr, area, view, cur)
	return cur
}

// ShortHelp implements [help.KeyMap].
func (n *Notifications) ShortHelp() []key.Binding {
	return []key.Binding{
		n.keyMap.UpDown,
		n.keyMap.Select,
		n.keyMap.Close,
	}
}

// FullHelp implements [help.KeyMap].
func (n *Notifications) FullHelp() [][]key.Binding {
	m := [][]key.Binding{}
	slice := []key.Binding{
		n.keyMap.Select,
		n.keyMap.Next,
		n.keyMap.Previous,
		n.keyMap.Close,
	}
	for i := 0; i < len(slice); i += 4 {
		end := min(i+4, len(slice))
		m = append(m, slice[i:end])
	}
	return m
}

func (n *Notifications) setItems() {
	cfg := n.com.Config()
	currentStyle := "auto"
	if cfg != nil && cfg.Options != nil && cfg.Options.Notifications != "" {
		currentStyle = cfg.Options.Notifications
	}

	items := make([]list.FilterableItem, 0, len(AllNotificationStyles))
	selectedIndex := 0
	for _, style := range AllNotificationStyles {
		// Native OS notifications don't build on every platform
		// (illumos/solaris); hide the option where it can't work.
		if style.ID == "native" && !notification.NativeSupported {
			continue
		}
		items = append(items, NewPickerItem(n.com.Styles, style.ID, style.Title, ""))
		if style.ID == currentStyle {
			selectedIndex = len(items)
		}
	}

	n.list.SetItems(items...)
	n.list.SetSelected(selectedIndex)
	n.list.ScrollToSelected()
}
