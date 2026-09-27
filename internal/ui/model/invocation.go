package model

import (
	"image"
	"slices"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/sahilm/fuzzy"
	"github.com/stubbedev/harness/internal/commands"
	"github.com/stubbedev/harness/internal/ui/dialog"
	"github.com/stubbedev/harness/internal/ui/keys"
	"github.com/stubbedev/harness/internal/ui/util"
)

// The command picker is the editor's side of command invocation: while
// the editor holds a command prefix and the start of a name ("/com"), it
// lists the commands that name could become just above the editor. The
// text stays in the editor; the picker only reads it. Picking works as
// in the palette (CommandItem.SelectAction): a command that takes
// arguments is completed so they can be typed after it, anything else
// runs.

// commandPickerMaxRows caps the rows the picker shows at once.
const commandPickerMaxRows = 8

// commandPicker is the picker's state.
type commandPicker struct {
	// open is set while the editor holds a partial invocation.
	open bool
	// items is the catalog, snapshotted when the picker opens.
	items []*dialog.CommandItem
	// query is the partial name matches were ranked for.
	query    string
	matches  []*dialog.CommandItem
	selected int
	// dismissed is the editor text the picker was closed on with esc;
	// it stays closed until the text changes.
	dismissed string
}

// visible reports whether the picker has rows to show, and so whether
// it owns the keys it handles.
func (p *commandPicker) visible() bool {
	return p.open && len(p.matches) > 0
}

// close closes the picker.
func (p *commandPicker) close() {
	*p = commandPicker{dismissed: p.dismissed}
}

// invocationPrefixes are the prefixes that open a command invocation in
// the editor: the single-character keys of the editor.commands binding,
// so a rebind moves them.
func (m *UI) invocationPrefixes() []string {
	var prefixes []string
	for _, k := range m.keyMap.Editor.Commands.Keys() {
		if utf8.RuneCountInString(k) == 1 {
			prefixes = append(prefixes, k)
		}
	}
	return prefixes
}

// invocationPrefix is the prefix commands are written with in the editor
// and the picker: the first of invocationPrefixes, empty when a rebind
// left none.
func (m *UI) invocationPrefix() string {
	if prefixes := m.invocationPrefixes(); len(prefixes) > 0 {
		return prefixes[0]
	}
	return ""
}

// commandCatalog returns the commands that apply to the current state.
func (m *UI) commandCatalog() *dialog.Catalog {
	var state dialog.CommandState
	if m.session != nil {
		state = dialog.CommandState{
			SessionID:  m.session.ID,
			HasSummary: m.session.SummaryMessageID != "",
			HasGoal:    m.session.Goal.Active(),
		}
	}
	return dialog.NewCatalog(m.com, state, m.customCommands, m.mcpPrompts)
}

// invocationsLive reports whether the editor reads invocations at all:
// it is focused on the prompt itself, not a shell command, an inline
// form, or an agent being steered.
func (m *UI) invocationsLive() bool {
	return m.focus == uiFocusEditor && !m.bangMode && m.activeInline == nil && m.agentView.shown == ""
}

// editorInvocation reads value as a command invocation. ok is false when
// value is an ordinary message: not shaped like one, or naming no
// command there is.
func (m *UI) editorInvocation(value string) (dialog.ActionInvoke, bool) {
	if !m.invocationsLive() {
		return dialog.ActionInvoke{}, false
	}
	name, raw, ok := commands.ParseInvocation(value, m.invocationPrefixes())
	if !ok {
		return dialog.ActionInvoke{}, false
	}
	item := dialog.FindInvocable(m.commandCatalog().Invocables(), name)
	if item == nil {
		return dialog.ActionInvoke{}, false
	}
	return dialog.ActionInvoke{Action: item.Action(), Name: item.SlashName(), Raw: raw}, true
}

// invoke runs a command invocation from the palette or the editor.
func (m *UI) invoke(inv dialog.ActionInvoke) tea.Cmd {
	action, err := dialog.Invoke(inv)
	if err != nil {
		return util.ReportWarn(err.Error())
	}
	return m.handleAction(action)
}

// closeCommandDialogs closes the palette and the arguments form, which
// have done their part once a command runs.
func (m *UI) closeCommandDialogs() {
	m.dialog.CloseDialog(dialog.CommandsID)
	m.dialog.CloseDialog(dialog.ArgumentsID)
}

// insertInvocation puts a command's name in the editor, followed by a
// space for its arguments.
func (m *UI) insertInvocation(name string) tea.Cmd {
	prefix := m.invocationPrefix()
	if prefix == "" {
		return nil
	}
	prevHeight := m.textarea.Height()
	m.textarea.SetValue(prefix + name + " ")
	m.textarea.MoveToEnd()
	m.commandPicker.close()
	return tea.Batch(m.focusEditor(), m.handleTextareaHeightChange(prevHeight))
}

// pickerQuery returns the partial command name the editor holds, if it
// holds one: a prefix and a name with nothing after it yet.
func (m *UI) pickerQuery() (string, bool) {
	if !m.invocationsLive() {
		return "", false
	}
	value := m.textarea.Value()
	for _, prefix := range m.invocationPrefixes() {
		rest, ok := strings.CutPrefix(value, prefix)
		if !ok {
			continue
		}
		if rest == "" || commands.IsCommandName(rest) {
			return rest, true
		}
		return "", false
	}
	return "", false
}

// refreshCommandPicker opens, updates or closes the picker for what the
// editor now holds. It runs after every edit.
func (m *UI) refreshCommandPicker() {
	p := &m.commandPicker
	value := m.textarea.Value()
	if value != p.dismissed {
		p.dismissed = ""
	}
	query, ok := m.pickerQuery()
	if !ok || p.dismissed != "" {
		p.close()
		return
	}
	if !p.open {
		p.open = true
		p.items = m.commandCatalog().Invocables()
		p.query = query
		p.matches = rankCommands(p.items, query)
		p.selected = 0
		return
	}
	if query != p.query {
		p.query = query
		p.matches = rankCommands(p.items, query)
		p.selected = 0
	}
}

// rankCommands orders the commands a partial name could become: those
// whose preferred name starts with it, then those with any name that
// does, then fuzzy matches. An empty name lists everything.
func rankCommands(items []*dialog.CommandItem, query string) []*dialog.CommandItem {
	if query == "" {
		return items
	}
	q := strings.ToLower(query)
	var primary, alias, rest []*dialog.CommandItem
	names := make([]string, 0, len(items))
	for _, item := range items {
		switch {
		case strings.HasPrefix(strings.ToLower(item.SlashName()), q):
			primary = append(primary, item)
		case slices.ContainsFunc(item.SlashNames(), func(n string) bool { return strings.HasPrefix(strings.ToLower(n), q) }):
			alias = append(alias, item)
		default:
			rest = append(rest, item)
			names = append(names, strings.Join(item.SlashNames(), " "))
		}
	}
	out := append(primary, alias...)
	for _, match := range fuzzy.Find(query, names) {
		out = append(out, rest[match.Index])
	}
	return out
}

// handleCommandPickerKey gives the picker the keys it owns while it is
// visible: moving the selection, completing, picking and dismissing.
// handled is false for every other key, which the editor then takes.
func (m *UI) handleCommandPickerKey(msg tea.KeyPressMsg) (cmd tea.Cmd, handled bool) {
	p := &m.commandPicker
	if !p.visible() {
		return nil, false
	}
	n := len(p.matches)
	switch {
	case key.Matches(msg, m.keyMap.Dialog.Next):
		p.selected = (p.selected + 1) % n
	case key.Matches(msg, m.keyMap.Dialog.Previous):
		p.selected = (p.selected - 1 + n) % n
	case key.Matches(msg, m.keyMap.Editor.CompleteCommand):
		return m.insertInvocation(p.matches[p.selected].SlashName()), true
	case key.Matches(msg, m.keyMap.Editor.SendMessage):
		item := p.matches[p.selected]
		if slices.ContainsFunc(item.SlashNames(), func(n string) bool { return strings.EqualFold(n, p.query) }) {
			// The name is typed out in full: send it as written, as
			// "/compact" with nothing after it.
			p.close()
			return nil, false
		}
		// The partial name is replaced by what picking does: the completed
		// invocation, or nothing once the command has run.
		action := item.SelectAction()
		prevHeight := m.textarea.Height()
		m.textarea.Reset()
		p.close()
		return tea.Batch(m.handleTextareaHeightChange(prevHeight), m.handleAction(action)), true
	case key.Matches(msg, m.keyMap.Dialog.Close):
		p.dismissed = m.textarea.Value()
		p.close()
	default:
		return nil, false
	}
	return nil, true
}

// commandPickerHelp returns the hints for the keys the picker owns.
func (m *UI) commandPickerHelp() []key.Binding {
	return []key.Binding{
		m.keyMap.Dialog.UpDown,
		keys.WithDesc(m.keyMap.Editor.SendMessage, "pick"),
		m.keyMap.Editor.CompleteCommand,
		m.keyMap.Dialog.Close,
	}
}

// drawCommandPicker draws the picker's rows directly above above, the
// top edge of the area below it, as wide as that area.
func (m *UI) drawCommandPicker(scr uv.Screen, above uv.Rectangle) {
	p := &m.commandPicker
	if !p.visible() || above.Min.Y <= 0 {
		return
	}
	rows := min(len(p.matches), commandPickerMaxRows, above.Min.Y)
	// The window ends at the selection once it is past the first rows, so
	// the selection is always among the rows shown.
	first := max(0, p.selected-rows+1)
	width := above.Dx()
	prefix := m.invocationPrefix()
	visible := p.matches[first : first+rows]
	nameWidth := 0
	for _, item := range visible {
		nameWidth = max(nameWidth, lipgloss.Width(prefix+item.SlashName()))
	}
	t := m.com.Styles
	lines := make([]string, 0, rows)
	for i, item := range visible {
		selected := first+i == p.selected
		style, hintStyle := t.Dialog.NormalItem, t.Dialog.SecondaryText
		if selected {
			style, hintStyle = t.Dialog.SelectedItem, t.Dialog.SelectedItem
		}
		name := prefix + item.SlashName()
		text := name + strings.Repeat(" ", nameWidth-lipgloss.Width(name))
		detail := strings.TrimSpace(item.ArgumentHint() + "  " + item.Description())
		inner := max(0, width-style.GetHorizontalFrameSize())
		line := style.Render(ansi.Truncate(text, inner, "…"))
		if room := inner - lipgloss.Width(text) - 2; detail != "" && room > 0 {
			line = style.Render(text+"  ") + hintStyle.Render(ansi.Truncate(detail, room, "…"))
		}
		if pad := width - lipgloss.Width(line); pad > 0 {
			line += style.Render(strings.Repeat(" ", pad))
		}
		lines = append(lines, line)
	}
	area := image.Rect(above.Min.X, above.Min.Y-rows, above.Max.X, above.Min.Y)
	uv.NewStyledString(strings.Join(lines, "\n")).Draw(scr, area)
}

// runCustomCommand runs a loaded command with its arguments: a skill is
// loaded and invoked with them, an extension produces its prompt from
// them, and a command file's content has them expanded into it.
func (m *UI) runCustomCommand(cmd commands.CustomCommand, args commands.Args) tea.Cmd {
	switch {
	case cmd.Skill != nil:
		return m.runSkill(cmd.Skill.SkillFilePath, cmd.Skill.Name, args.Raw)
	case cmd.ExtensionID != "":
		// An extension command has no content until the extension
		// produces it, which may touch the filesystem or the network, so
		// the expansion happens off the UI loop.
		return m.runExtensionCommand(cmd.Name, cmd.ExtensionID, args.Values)
	}
	return util.CmdHandler(sendMessageMsg{Name: cmd.Name, Content: args.Expand(cmd.Content)})
}
