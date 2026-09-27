package model

import (
	"fmt"
	"hash/fnv"
	"image"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/util"
)

// DefaultStatusTTL is the default time-to-live for status messages.
const DefaultStatusTTL = 5 * time.Second

// Status is the status bar and help model.
type Status struct {
	com    *common.Common
	help   help.Model
	helpKm help.KeyMap
	msg    util.InfoMsg

	// helpLine memoizes the keymap's rendered help line. Draw runs on
	// every frame, and help.View plus its styling ran on each one;
	// the line is rebuilt only when an input to it changed, and every
	// input lives in helpKey.
	helpLine string
	helpKey  statusHelpKey
	helpSet  bool
}

// statusHelpKey identifies the rendered help line. Width is the draw
// area's width (the wrapper pads to it) and helpWidth the help model's
// truncation width (SetWidth); showAll picks the short or full view;
// binds hashes the live keymap's bindings, which track mode, focus,
// dialogs and open panels; styles fingerprints the styles the line is
// rendered through, so a theme change re-renders it.
type statusHelpKey struct {
	width     int
	helpWidth int
	showAll   bool
	binds     uint64
	styles    uint64
}

// NewStatus creates a new status bar and help model.
func NewStatus(com *common.Common, km help.KeyMap) *Status {
	s := new(Status)
	s.com = com
	s.help = help.New()
	s.help.Styles = com.Styles.Help
	s.helpKm = km
	return s
}

// SetInfoMsg sets the status info message.
func (s *Status) SetInfoMsg(msg util.InfoMsg) {
	s.msg = msg
}

// ClearInfoMsg clears the status info message.
func (s *Status) ClearInfoMsg() {
	s.msg = util.InfoMsg{}
}

// SetWidth sets the width of the status bar and help view.
func (s *Status) SetWidth(width int) {
	helpStyle := s.com.Styles.Status.Help
	horizontalPadding := helpStyle.GetPaddingLeft() + helpStyle.GetPaddingRight()
	s.help.SetWidth(width - horizontalPadding)
}

// ShowingAll returns whether the full help view is shown.
func (s *Status) ShowingAll() bool {
	return s.help.ShowAll
}

// ToggleHelp toggles the full help view.
func (s *Status) ToggleHelp() {
	s.help.ShowAll = !s.help.ShowAll
}

// Draw draws the status bar onto the screen.
//
// The help (and the info message drawn over it) is anchored at the
// bottom of the area so the hints always hug the terminal's last row.
func (s *Status) Draw(scr uv.Screen, area uv.Rectangle) {
	helpView := s.com.Styles.Status.Help.Render(s.helpView(area.Dx()))
	uv.NewStyledString(helpView).Draw(scr, bottomRect(area, lipgloss.Height(helpView)))

	// Render notifications
	if s.msg.IsEmpty() {
		return
	}

	var indStyle lipgloss.Style
	var msgStyle lipgloss.Style
	switch s.msg.Type {
	case util.InfoTypeError:
		indStyle = s.com.Styles.Status.ErrorIndicator
		msgStyle = s.com.Styles.Status.ErrorMessage
	case util.InfoTypeWarn:
		indStyle = s.com.Styles.Status.WarnIndicator
		msgStyle = s.com.Styles.Status.WarnMessage
	case util.InfoTypeUpdate:
		indStyle = s.com.Styles.Status.UpdateIndicator
		msgStyle = s.com.Styles.Status.UpdateMessage
	case util.InfoTypeInfo:
		indStyle = s.com.Styles.Status.InfoIndicator
		msgStyle = s.com.Styles.Status.InfoMessage
	case util.InfoTypeSuccess:
		indStyle = s.com.Styles.Status.SuccessIndicator
		msgStyle = s.com.Styles.Status.SuccessMessage
	}

	ind := indStyle.String()
	indWidth := lipgloss.Width(ind)
	msgPad := msgStyle.GetPaddingLeft() + msgStyle.GetPaddingRight()
	avail := max(0, area.Dx()-indWidth-msgPad)
	msg := strings.Join(strings.Split(s.msg.Msg, "\n"), " ")
	msg = ansi.Truncate(msg, avail, "…")
	if w := lipgloss.Width(msg); w < avail {
		msg += strings.Repeat(" ", avail-w)
	}
	info := msgStyle.Render(msg)

	// Draw the info message over the help view
	uv.NewStyledString(ind+info).Draw(scr, bottomRect(area, 1))
}

// helpView returns the keymap's help line for a draw area of the given
// width, memoized across frames (see statusHelpKey).
func (s *Status) helpView(width int) string {
	key := statusHelpKey{
		width:     width,
		helpWidth: s.help.Width(),
		showAll:   s.help.ShowAll,
		binds:     s.bindsToken(),
		styles:    s.stylesToken(),
	}
	if !s.helpSet || s.helpKey != key {
		s.helpLine = s.help.View(s.helpKm)
		s.helpKey = key
		s.helpSet = true
	}
	return s.helpLine
}

// bindsToken folds the live keymap's hint state into one number: every
// binding's enabled flag, key and description, in the order help.View
// renders them (full help's groups separated by a marker). A hint
// change — mode, focus, a dialog or panel opening, a rebind — moves
// the token.
func (s *Status) bindsToken() uint64 {
	h := fnv.New64a()
	hashGroup := func(bindings []key.Binding) {
		for _, b := range bindings {
			hlp := b.Help()
			h.Write([]byte{enabledBit(b)})
			h.Write([]byte(hlp.Key))
			h.Write([]byte{0})
			h.Write([]byte(hlp.Desc))
			h.Write([]byte{0})
		}
		h.Write([]byte{0})
	}
	if s.help.ShowAll {
		for _, group := range s.helpKm.FullHelp() {
			hashGroup(group)
		}
		return h.Sum64()
	}
	hashGroup(s.helpKm.ShortHelp())
	return h.Sum64()
}

// enabledBit returns the byte a binding hashes to for its enabled flag.
func enabledBit(b key.Binding) byte {
	if b.Enabled() {
		return '1'
	}
	return '0'
}

// stylesToken fingerprints the inputs that decide how the help line is
// rendered rather than what it says: the help model's key/desc/
// separator styles, its separator and ellipsis literals, and the
// wrapper style. Themes restyle them together, so a theme switch moves
// the token and the cached line re-renders in the new theme.
func (s *Status) stylesToken() uint64 {
	h := fnv.New64a()
	wrap := s.com.Styles.Status.Help
	sty := s.help.Styles
	fmt.Fprint(h,
		wrap.GetForeground(), wrap.GetBackground(),
		wrap.GetPaddingLeft(), wrap.GetPaddingRight(),
		s.help.ShortSeparator, s.help.FullSeparator, s.help.Ellipsis,
		sty.Ellipsis.GetForeground(),
		sty.ShortKey.GetForeground(), sty.ShortDesc.GetForeground(),
		sty.ShortSeparator.GetForeground(),
		sty.FullKey.GetForeground(), sty.FullDesc.GetForeground(),
		sty.FullSeparator.GetForeground())
	return h.Sum64()
}

// bottomRect returns the bottom-most rows of area with the given
// height, clamped to the area itself.
func bottomRect(area uv.Rectangle, height int) uv.Rectangle {
	return image.Rect(area.Min.X, max(area.Min.Y, area.Max.Y-height), area.Max.X, area.Max.Y)
}

// clearInfoMsgCmd returns a command that clears the info message after the
// given TTL.
func clearInfoMsgCmd(ttl time.Duration) tea.Cmd {
	return tea.Tick(ttl, func(time.Time) tea.Msg {
		return util.ClearStatusMsg{}
	})
}
