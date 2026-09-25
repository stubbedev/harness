// Package keys holds every key binding the TUI consults, so a binding is
// defined, named for options.tui.keybinds, and rebound in one place.
package keys

import (
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"charm.land/bubbles/v2/key"
)

type KeyMap struct {
	Editor struct {
		SendMessage key.Binding
		OpenEditor  key.Binding
		Newline     key.Binding
		MentionFile key.Binding
		Commands    key.Binding
		Skills      key.Binding

		// ShellMode enters bang mode: the prompt becomes a shell command.
		ShellMode key.Binding

		// Escape leaves rewind arming and history preview in the editor.
		Escape key.Binding

		// History navigation.
		HistoryPrev key.Binding
		HistoryNext key.Binding

		// CopySelection copies the current textarea selection to the
		// clipboard.
		CopySelection key.Binding

		// CutSelection copies the current textarea selection to the
		// clipboard and deletes it from the textarea.
		CutSelection key.Binding

		// SelectAll selects all text in the textarea.
		SelectAll key.Binding

		// SelectLineUp and SelectLineDown extend the textarea selection
		// one display row at a time. Shift+up also leaves the editor from
		// the top row, through the chat up-one-item binding.
		SelectLineUp   key.Binding
		SelectLineDown key.Binding

		// Paste pastes the clipboard into the textarea: an image
		// becomes an inline attachment token, text flows through the
		// text paste pipeline.
		Paste key.Binding

		// DeleteWordBackward deletes from the cursor back to the start
		// of the word.
		DeleteWordBackward key.Binding

		// LineStart moves the cursor to the start of the line.
		LineStart key.Binding
	}

	Chat struct {
		NewSession      key.Binding
		Cancel          key.Binding
		Details         key.Binding
		TogglePills     key.Binding
		Down            key.Binding
		Up              key.Binding
		UpDown          key.Binding
		DownOneItem     key.Binding
		UpOneItem       key.Binding
		UpDownOneItem   key.Binding
		PageDown        key.Binding
		PageUp          key.Binding
		HalfPageDown    key.Binding
		HalfPageUp      key.Binding
		Home            key.Binding
		End             key.Binding
		EndFollow       key.Binding
		Copy            key.Binding
		ClearHighlight  key.Binding
		Expand          key.Binding
		DigIn           key.Binding
		ScrollLeft      key.Binding
		ScrollRight     key.Binding
		BackgroundTasks key.Binding
	}

	// Dialog holds the bindings every dialog consults.
	Dialog DialogKeys

	// Completions holds the bindings for the inline completions popup.
	Completions CompletionsKeys

	// Global key maps
	Quit          key.Binding
	Help          key.Binding
	Commands      key.Binding
	Models        key.Binding
	Suspend       key.Binding
	Sessions      key.Binding
	Tab           key.Binding
	ShiftTab      key.Binding
	ParentSession key.Binding
	// ExportConversation writes the current session transcript to a
	// markdown file and copies it to the clipboard.
	ExportConversation key.Binding
	// Themes opens the color theme picker.
	Themes key.Binding
}

// DialogKeys are the bindings dialogs consult. The five at the top are
// shared: a dialog that only picks from a list binds nothing of its own.
// The groups below them cover keys a single dialog has.
type DialogKeys struct {
	Select   key.Binding
	Next     key.Binding
	Previous key.Binding
	UpDown   key.Binding
	Close    key.Binding

	Models    ModelsDialogKeys
	Commands  CommandsDialogKeys
	Sessions  SessionsDialogKeys
	Rewind    RewindDialogKeys
	Arguments ArgumentsDialogKeys
	OAuth     OAuthDialogKeys
	MCPAuth   MCPAuthDialogKeys
	Question  QuestionDialogKeys

	MCPServers MCPServersDialogKeys
	LSPServers LSPServersDialogKeys
}

// ModelsDialogKeys are the model picker's own bindings.
type ModelsDialogKeys struct {
	ToggleType key.Binding
	Edit       key.Binding
	Connect    key.Binding
}

// CommandsDialogKeys are the command palette's own bindings.
type CommandsDialogKeys struct {
	Tab      key.Binding
	ShiftTab key.Binding
}

// SessionsDialogKeys are the session list's own bindings, including the
// rename and delete confirmations.
type SessionsDialogKeys struct {
	Select        key.Binding
	Delete        key.Binding
	Rename        key.Binding
	ConfirmRename key.Binding
	CancelRename  key.Binding
	KeepTitle     key.Binding
	ConfirmDelete key.Binding
	CancelDelete  key.Binding
}

// RewindDialogKeys are the rewind picker's own bindings.
type RewindDialogKeys struct {
	Select key.Binding
	Back   key.Binding
}

// ArgumentsDialogKeys are the command-arguments form's own bindings.
type ArgumentsDialogKeys struct {
	Confirm  key.Binding
	Next     key.Binding
	Previous key.Binding
}

// OAuthDialogKeys are the device-flow dialog's own bindings.
type OAuthDialogKeys struct {
	Copy    key.Binding
	CopyURL key.Binding
}

// MCPAuthDialogKeys are the MCP authorization dialog's own bindings.
type MCPAuthDialogKeys struct {
	Copy key.Binding
	Skip key.Binding
}

// MCPServersDialogKeys are the MCP server manager's own bindings.
type MCPServersDialogKeys struct {
	Select key.Binding
	Back   key.Binding
}

// LSPServersDialogKeys are the LSP server manager's own bindings.
type LSPServersDialogKeys struct {
	Select key.Binding
	Back   key.Binding
}

// QuestionDialogKeys are the bindings shared by the question dialogs the
// agent raises: yes/no, single and multiple choice, free text, and forms.
type QuestionDialogKeys struct {
	Up      key.Binding
	Down    key.Binding
	Left    key.Binding
	Right   key.Binding
	Confirm key.Binding
	Yes     key.Binding
	No      key.Binding
	Note    key.Binding
	Toggle  key.Binding
	Newline key.Binding
	PrevTab key.Binding
	NextTab key.Binding
}

// CompletionsKeys are the inline completion popup's bindings.
type CompletionsKeys struct {
	Up         key.Binding
	Down       key.Binding
	Select     key.Binding
	Cancel     key.Binding
	UpInsert   key.Binding
	DownInsert key.Binding
}

// DefaultKeyMap returns the built-in bindings, before any user overrides.
func DefaultKeyMap() KeyMap {
	km := KeyMap{
		Quit: key.NewBinding(
			key.WithKeys("ctrl+c"),
			key.WithHelp("ctrl+c", "quit"),
		),
		Help: key.NewBinding(
			key.WithKeys("ctrl+g"),
			key.WithHelp("ctrl+g", "more"),
		),
		Commands: key.NewBinding(
			key.WithKeys("ctrl+p"),
			key.WithHelp("ctrl+p", "commands"),
		),
		Models: key.NewBinding(
			key.WithKeys("ctrl+m", "ctrl+l"),
			key.WithHelp("ctrl+l", "models"),
		),
		Suspend: key.NewBinding(
			key.WithKeys("ctrl+z"),
			key.WithHelp("ctrl+z", "suspend"),
		),
		Sessions: key.NewBinding(
			key.WithKeys("ctrl+s"),
			key.WithHelp("ctrl+s", "sessions"),
		),
		Tab: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "change focus"),
		),
		ShiftTab: key.NewBinding(
			key.WithKeys("shift+tab"),
			key.WithHelp("shift+tab", "change focus"),
		),
		ParentSession: key.NewBinding(
			key.WithKeys("ctrl+up"),
			key.WithHelp("ctrl+up", "go to parent session"),
		),
		ExportConversation: key.NewBinding(
			key.WithKeys("ctrl+shift+e"),
			key.WithHelp("ctrl+shift+e", "export conversation"),
		),
		// The ctrl+ space is crowded (ctrl+t already toggles tasks) and the
		// textarea claims most of what is left, so the theme picker sits on
		// ctrl+shift+t. Alt chords are avoided in the defaults because they
		// are unreliable across terminal setups.
		Themes: key.NewBinding(
			key.WithKeys("ctrl+shift+t"),
			key.WithHelp("ctrl+shift+t", "themes"),
		),
	}

	km.Editor.SendMessage = key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "send"),
	)
	km.Editor.OpenEditor = key.NewBinding(
		key.WithKeys("ctrl+o"),
		key.WithHelp("ctrl+o", "open editor"),
	)
	km.Editor.Newline = key.NewBinding(
		key.WithKeys("shift+enter", "ctrl+j"),
		// "ctrl+j" is the fallback for terminals that cannot send
		// "shift+enter", so the hint names the key users actually press.
		key.WithHelp("shift+enter", "newline"),
	)
	km.Editor.Paste = key.NewBinding(
		key.WithKeys("ctrl+v", "ctrl+shift+v"),
		key.WithHelp("ctrl+v", "paste"),
	)
	km.Editor.DeleteWordBackward = key.NewBinding(
		key.WithKeys("ctrl+backspace", "ctrl+w"),
		key.WithHelp("ctrl+backspace", "delete word"),
	)
	km.Editor.MentionFile = key.NewBinding(
		key.WithKeys("@"),
		key.WithHelp("@", "mention file"),
	)
	km.Editor.Commands = key.NewBinding(
		key.WithKeys(":"),
		key.WithHelp(":", "command palette"),
	)
	km.Editor.Skills = key.NewBinding(
		key.WithKeys("/"),
		key.WithHelp("/", "skills"),
	)
	km.Editor.ShellMode = key.NewBinding(
		key.WithKeys("!"),
		key.WithHelp("!", "shell mode"),
	)
	km.Editor.Escape = key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "rewind"),
	)
	km.Editor.HistoryPrev = key.NewBinding(
		key.WithKeys("up"),
		key.WithHelp("↑", "previous prompt"),
	)
	km.Editor.HistoryNext = key.NewBinding(
		key.WithKeys("down"),
		key.WithHelp("↓", "next prompt"),
	)
	km.Editor.CopySelection = key.NewBinding(
		key.WithKeys("ctrl+shift+c"),
		key.WithHelp("ctrl+shift+c", "copy selection"),
	)
	km.Editor.CutSelection = key.NewBinding(
		key.WithKeys("ctrl+shift+x"),
		key.WithHelp("ctrl+shift+x", "cut selection"),
	)
	km.Editor.SelectAll = key.NewBinding(
		key.WithKeys("ctrl+shift+a"),
		key.WithHelp("ctrl+shift+a", "select all"),
	)
	km.Editor.SelectLineUp = key.NewBinding(
		key.WithKeys("shift+up"),
		key.WithHelp("shift+↑", "select up"),
	)
	km.Editor.SelectLineDown = key.NewBinding(
		key.WithKeys("shift+down"),
		key.WithHelp("shift+↓", "select down"),
	)
	km.Editor.LineStart = key.NewBinding(
		key.WithKeys("home", "ctrl+a"),
		key.WithHelp("home", "line start"),
	)

	km.Chat.NewSession = key.NewBinding(
		key.WithKeys("ctrl+n"),
		key.WithHelp("ctrl+n", "new session"),
	)
	km.Chat.Cancel = key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "cancel"),
	)
	km.Chat.Details = key.NewBinding(
		key.WithKeys("ctrl+d"),
		key.WithHelp("ctrl+d", "toggle details"),
	)
	km.Chat.TogglePills = key.NewBinding(
		key.WithKeys("ctrl+t", "ctrl+space"),
		key.WithHelp("ctrl+t", "toggle tasks"),
	)

	km.Chat.Down = key.NewBinding(
		key.WithKeys("down", "ctrl+j", "j"),
		key.WithHelp("↓", "down"),
	)
	km.Chat.Up = key.NewBinding(
		key.WithKeys("up", "ctrl+k", "k"),
		key.WithHelp("↑", "up"),
	)
	km.Chat.UpDown = key.NewBinding(
		key.WithKeys("up", "down"),
		key.WithHelp("↑↓", "scroll"),
	)
	km.Chat.UpOneItem = key.NewBinding(
		key.WithKeys("shift+up", "K"),
		key.WithHelp("shift+↑", "up one item"),
	)
	km.Chat.DownOneItem = key.NewBinding(
		key.WithKeys("shift+down", "J"),
		key.WithHelp("shift+↓", "down one item"),
	)
	km.Chat.UpDownOneItem = key.NewBinding(
		key.WithKeys("shift+up", "shift+down"),
		key.WithHelp("shift+↑↓", "scroll one item"),
	)
	km.Chat.HalfPageDown = key.NewBinding(
		key.WithKeys("d"),
		key.WithHelp("d", "half page down"),
	)
	km.Chat.PageDown = key.NewBinding(
		key.WithKeys("pgdown", " ", "f"),
		key.WithHelp("f/pgdn", "page down"),
	)
	km.Chat.PageUp = key.NewBinding(
		key.WithKeys("pgup", "b"),
		key.WithHelp("b/pgup", "page up"),
	)
	km.Chat.HalfPageUp = key.NewBinding(
		key.WithKeys("u"),
		key.WithHelp("u", "half page up"),
	)
	km.Chat.Home = key.NewBinding(
		key.WithKeys("g", "home"),
		key.WithHelp("g", "home"),
	)
	km.Chat.End = key.NewBinding(
		key.WithKeys("G", "end"),
		key.WithHelp("G", "end"),
	)
	km.Chat.EndFollow = key.NewBinding(
		key.WithKeys("ctrl+end"),
		key.WithHelp("ctrl+end", "end follow"),
	)
	km.Chat.Copy = key.NewBinding(
		key.WithKeys("c", "y", "C", "Y"),
		key.WithHelp("c/y", "copy"),
	)
	km.Chat.ClearHighlight = key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "clear selection"),
	)
	km.Chat.Expand = key.NewBinding(
		key.WithKeys("space"),
		key.WithHelp("space", "toggle"),
	)
	km.Chat.DigIn = key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("↵", "go in"),
	)
	km.Chat.ScrollLeft = key.NewBinding(
		key.WithKeys("shift+left", "H"),
		key.WithHelp("shift+←/H", "scroll left"),
	)
	km.Chat.ScrollRight = key.NewBinding(
		key.WithKeys("shift+right", "L"),
		key.WithHelp("shift+→/L", "scroll right"),
	)
	km.Chat.BackgroundTasks = key.NewBinding(
		key.WithKeys("ctrl+b"),
		key.WithHelp("ctrl+b", "background tasks"),
	)

	km.Dialog.Select = key.NewBinding(
		key.WithKeys("enter", "ctrl+y"),
		key.WithHelp("enter", "confirm"),
	)
	km.Dialog.Next = key.NewBinding(
		key.WithKeys("down", "ctrl+n"),
		key.WithHelp("↓", "next item"),
	)
	km.Dialog.Previous = key.NewBinding(
		key.WithKeys("up", "ctrl+p"),
		key.WithHelp("↑", "previous item"),
	)
	km.Dialog.UpDown = key.NewBinding(
		key.WithKeys("up", "down"),
		key.WithHelp("↑/↓", "choose"),
	)
	km.Dialog.Close = key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "exit"),
	)

	km.Dialog.Models.ToggleType = key.NewBinding(
		key.WithKeys("tab", "shift+tab"),
		key.WithHelp("tab", "toggle type"),
	)
	km.Dialog.Models.Edit = key.NewBinding(
		key.WithKeys("ctrl+e"),
		key.WithHelp("ctrl+e", "edit"),
	)
	km.Dialog.Models.Connect = key.NewBinding(
		key.WithKeys("ctrl+g"),
		key.WithHelp("ctrl+g", "connect"),
	)

	km.Dialog.Commands.Tab = key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "switch selection"),
	)
	km.Dialog.Commands.ShiftTab = key.NewBinding(
		key.WithKeys("shift+tab"),
		key.WithHelp("shift+tab", "switch selection prev"),
	)

	km.Dialog.Sessions.Select = key.NewBinding(
		key.WithKeys("enter", "tab", "ctrl+y"),
		key.WithHelp("enter", "choose"),
	)
	km.Dialog.Sessions.Delete = key.NewBinding(
		key.WithKeys("ctrl+x"),
		key.WithHelp("ctrl+x", "delete"),
	)
	km.Dialog.Sessions.Rename = key.NewBinding(
		key.WithKeys("ctrl+r"),
		key.WithHelp("ctrl+r", "rename"),
	)
	km.Dialog.Sessions.ConfirmRename = key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "confirm"),
	)
	km.Dialog.Sessions.CancelRename = key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "cancel"),
	)
	km.Dialog.Sessions.KeepTitle = key.NewBinding(
		key.WithKeys("space"),
		key.WithHelp("space", "keep current title"),
	)
	km.Dialog.Sessions.ConfirmDelete = key.NewBinding(
		key.WithKeys("y", "enter"),
		key.WithHelp("y", "delete"),
	)
	km.Dialog.Sessions.CancelDelete = key.NewBinding(
		key.WithKeys("n", "esc"),
		key.WithHelp("n", "cancel"),
	)

	km.Dialog.Rewind.Select = key.NewBinding(
		key.WithKeys("enter", "tab", "ctrl+y"),
		key.WithHelp("enter", "choose"),
	)
	km.Dialog.Rewind.Back = key.NewBinding(
		key.WithKeys("esc", "h", "backspace"),
		key.WithHelp("esc", "back"),
	)

	km.Dialog.MCPServers.Select = key.NewBinding(
		key.WithKeys("enter", "tab", "ctrl+y"),
		key.WithHelp("enter", "choose"),
	)
	km.Dialog.MCPServers.Back = key.NewBinding(
		key.WithKeys("esc", "h", "backspace"),
		key.WithHelp("esc", "back"),
	)

	km.Dialog.LSPServers.Select = key.NewBinding(
		key.WithKeys("enter", "tab", "ctrl+y"),
		key.WithHelp("enter", "choose"),
	)
	km.Dialog.LSPServers.Back = key.NewBinding(
		key.WithKeys("esc", "h", "backspace"),
		key.WithHelp("esc", "back"),
	)

	km.Dialog.Arguments.Confirm = key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "confirm"),
	)
	km.Dialog.Arguments.Next = key.NewBinding(
		key.WithKeys("down", "tab"),
		key.WithHelp("↓/tab", "next"),
	)
	km.Dialog.Arguments.Previous = key.NewBinding(
		key.WithKeys("up", "shift+tab"),
		key.WithHelp("↑/shift+tab", "previous"),
	)

	km.Dialog.OAuth.Copy = key.NewBinding(
		key.WithKeys("c"),
		key.WithHelp("c", "copy code"),
	)
	km.Dialog.OAuth.CopyURL = key.NewBinding(
		key.WithKeys("u"),
		key.WithHelp("u", "copy url"),
	)

	km.Dialog.MCPAuth.Copy = key.NewBinding(
		key.WithKeys("c", "u"),
		key.WithHelp("c", "copy url"),
	)
	km.Dialog.MCPAuth.Skip = key.NewBinding(
		key.WithKeys("s"),
		key.WithHelp("s", "skip"),
	)

	km.Dialog.Question.Up = key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑", "up"),
	)
	km.Dialog.Question.Down = key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓", "down"),
	)
	km.Dialog.Question.Left = key.NewBinding(
		key.WithKeys("left"),
		key.WithHelp("←/→", "switch"),
	)
	km.Dialog.Question.Right = key.NewBinding(
		key.WithKeys("right"),
		key.WithHelp("←/→", "switch"),
	)
	km.Dialog.Question.Confirm = key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "confirm"),
	)
	km.Dialog.Question.Yes = key.NewBinding(
		key.WithKeys("y", "Y"),
		key.WithHelp("y", "yes"),
	)
	km.Dialog.Question.No = key.NewBinding(
		key.WithKeys("n", "N"),
		key.WithHelp("n", "no"),
	)
	km.Dialog.Question.Note = key.NewBinding(
		key.WithKeys("ctrl+n"),
		key.WithHelp("ctrl+n", "note"),
	)
	km.Dialog.Question.Toggle = key.NewBinding(
		key.WithKeys(" ", "space"),
		key.WithHelp("space", "toggle"),
	)
	km.Dialog.Question.Newline = key.NewBinding(
		key.WithKeys("shift+enter", "ctrl+j"),
		key.WithHelp("shift+enter", "newline"),
	)
	km.Dialog.Question.PrevTab = key.NewBinding(
		key.WithKeys("[", "ctrl+left"),
		key.WithHelp("[", "prev tab"),
	)
	km.Dialog.Question.NextTab = key.NewBinding(
		key.WithKeys("]", "ctrl+right"),
		key.WithHelp("]", "next tab"),
	)

	km.Completions.Down = key.NewBinding(
		key.WithKeys("down"),
		key.WithHelp("down", "move down"),
	)
	km.Completions.Up = key.NewBinding(
		key.WithKeys("up"),
		key.WithHelp("up", "move up"),
	)
	km.Completions.Select = key.NewBinding(
		key.WithKeys("enter", "tab", "ctrl+y"),
		key.WithHelp("enter", "select"),
	)
	km.Completions.Cancel = key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "cancel"),
	)
	km.Completions.DownInsert = key.NewBinding(
		key.WithKeys("ctrl+n"),
		key.WithHelp("ctrl+n", "insert next"),
	)
	km.Completions.UpInsert = key.NewBinding(
		key.WithKeys("ctrl+p"),
		key.WithHelp("ctrl+p", "insert previous"),
	)

	return km
}

// WithDesc returns a copy of b under a different help description. Dialogs
// share one binding but describe it in their own words ("preview" in the
// theme picker, "connect" in the provider list), and the description is the
// only part that may differ: rebinding the action still moves every copy.
func WithDesc(b key.Binding, desc string) key.Binding {
	return key.NewBinding(
		key.WithKeys(b.Keys()...),
		key.WithHelp(b.Help().Key, desc),
	)
}

// Merge returns a binding that fires on any of the given bindings' keys,
// described by the first one's help. Used where a dialog treats several
// actions as one prompt ("enter/esc to close") or navigates with the union
// of its arrow keys.
func Merge(desc string, bindings ...key.Binding) key.Binding {
	var all []string
	for _, b := range bindings {
		all = append(all, b.Keys()...)
	}
	helpKey := ""
	if len(bindings) > 0 {
		helpKey = bindings[0].Help().Key
	}
	return key.NewBinding(
		key.WithKeys(all...),
		key.WithHelp(helpKey, desc),
	)
}

// keybindActions maps the stable action names accepted by
// options.tui.keybinds to the bindings they override. Names mirror the
// KeyMap fields: the global group is bare, the editor group carries the
// "editor." prefix, and the chat group the "chat." prefix.
func (km *KeyMap) keybindActions() map[string]*key.Binding {
	return map[string]*key.Binding{
		"quit":                        &km.Quit,
		"help":                        &km.Help,
		"commands":                    &km.Commands,
		"models":                      &km.Models,
		"suspend":                     &km.Suspend,
		"sessions":                    &km.Sessions,
		"tab":                         &km.Tab,
		"shift_tab":                   &km.ShiftTab,
		"parent_session":              &km.ParentSession,
		"export_conversation":         &km.ExportConversation,
		"themes":                      &km.Themes,
		"editor.send_message":         &km.Editor.SendMessage,
		"editor.open_editor":          &km.Editor.OpenEditor,
		"editor.newline":              &km.Editor.Newline,
		"editor.mention_file":         &km.Editor.MentionFile,
		"editor.commands":             &km.Editor.Commands,
		"editor.skills":               &km.Editor.Skills,
		"editor.shell_mode":           &km.Editor.ShellMode,
		"editor.escape":               &km.Editor.Escape,
		"editor.history_prev":         &km.Editor.HistoryPrev,
		"editor.history_next":         &km.Editor.HistoryNext,
		"editor.copy_selection":       &km.Editor.CopySelection,
		"editor.cut_selection":        &km.Editor.CutSelection,
		"editor.select_all":           &km.Editor.SelectAll,
		"editor.select_line_up":       &km.Editor.SelectLineUp,
		"editor.select_line_down":     &km.Editor.SelectLineDown,
		"editor.paste":                &km.Editor.Paste,
		"editor.delete_word_backward": &km.Editor.DeleteWordBackward,
		"editor.line_start":           &km.Editor.LineStart,
		"chat.new_session":            &km.Chat.NewSession,
		"chat.cancel":                 &km.Chat.Cancel,
		"chat.details":                &km.Chat.Details,
		"chat.toggle_pills":           &km.Chat.TogglePills,
		"chat.down":                   &km.Chat.Down,
		"chat.up":                     &km.Chat.Up,
		"chat.up_down":                &km.Chat.UpDown,
		"chat.down_one_item":          &km.Chat.DownOneItem,
		"chat.up_one_item":            &km.Chat.UpOneItem,
		"chat.up_down_one_item":       &km.Chat.UpDownOneItem,
		"chat.page_down":              &km.Chat.PageDown,
		"chat.page_up":                &km.Chat.PageUp,
		"chat.half_page_down":         &km.Chat.HalfPageDown,
		"chat.half_page_up":           &km.Chat.HalfPageUp,
		"chat.home":                   &km.Chat.Home,
		"chat.end":                    &km.Chat.End,
		"chat.end_follow":             &km.Chat.EndFollow,
		"chat.copy":                   &km.Chat.Copy,
		"chat.clear_highlight":        &km.Chat.ClearHighlight,
		"chat.expand":                 &km.Chat.Expand,
		"chat.dig_in":                 &km.Chat.DigIn,
		"chat.scroll_left":            &km.Chat.ScrollLeft,
		"chat.scroll_right":           &km.Chat.ScrollRight,
		"chat.background_tasks":       &km.Chat.BackgroundTasks,

		"dialog.select":                  &km.Dialog.Select,
		"dialog.next":                    &km.Dialog.Next,
		"dialog.previous":                &km.Dialog.Previous,
		"dialog.up_down":                 &km.Dialog.UpDown,
		"dialog.close":                   &km.Dialog.Close,
		"dialog.models.toggle_type":      &km.Dialog.Models.ToggleType,
		"dialog.models.edit":             &km.Dialog.Models.Edit,
		"dialog.models.connect":          &km.Dialog.Models.Connect,
		"dialog.commands.tab":            &km.Dialog.Commands.Tab,
		"dialog.commands.shift_tab":      &km.Dialog.Commands.ShiftTab,
		"dialog.sessions.select":         &km.Dialog.Sessions.Select,
		"dialog.sessions.delete":         &km.Dialog.Sessions.Delete,
		"dialog.sessions.rename":         &km.Dialog.Sessions.Rename,
		"dialog.sessions.confirm_rename": &km.Dialog.Sessions.ConfirmRename,
		"dialog.sessions.cancel_rename":  &km.Dialog.Sessions.CancelRename,
		"dialog.sessions.keep_title":     &km.Dialog.Sessions.KeepTitle,
		"dialog.sessions.confirm_delete": &km.Dialog.Sessions.ConfirmDelete,
		"dialog.sessions.cancel_delete":  &km.Dialog.Sessions.CancelDelete,
		"dialog.rewind.select":           &km.Dialog.Rewind.Select,
		"dialog.rewind.back":             &km.Dialog.Rewind.Back,
		"dialog.arguments.confirm":       &km.Dialog.Arguments.Confirm,
		"dialog.arguments.next":          &km.Dialog.Arguments.Next,
		"dialog.arguments.previous":      &km.Dialog.Arguments.Previous,
		"dialog.oauth.copy":              &km.Dialog.OAuth.Copy,
		"dialog.oauth.copy_url":          &km.Dialog.OAuth.CopyURL,
		"dialog.mcp_auth.copy":           &km.Dialog.MCPAuth.Copy,
		"dialog.mcp_auth.skip":           &km.Dialog.MCPAuth.Skip,
		"dialog.question.up":             &km.Dialog.Question.Up,
		"dialog.question.down":           &km.Dialog.Question.Down,
		"dialog.question.left":           &km.Dialog.Question.Left,
		"dialog.question.right":          &km.Dialog.Question.Right,
		"dialog.question.confirm":        &km.Dialog.Question.Confirm,
		"dialog.question.yes":            &km.Dialog.Question.Yes,
		"dialog.question.no":             &km.Dialog.Question.No,
		"dialog.question.note":           &km.Dialog.Question.Note,
		"dialog.question.toggle":         &km.Dialog.Question.Toggle,
		"dialog.question.newline":        &km.Dialog.Question.Newline,
		"dialog.question.prev_tab":       &km.Dialog.Question.PrevTab,
		"dialog.question.next_tab":       &km.Dialog.Question.NextTab,
		"dialog.mcp_servers.select":      &km.Dialog.MCPServers.Select,
		"dialog.mcp_servers.back":        &km.Dialog.MCPServers.Back,
		"dialog.lsp_servers.select":      &km.Dialog.LSPServers.Select,
		"dialog.lsp_servers.back":        &km.Dialog.LSPServers.Back,

		"completions.up":              &km.Completions.Up,
		"completions.down":            &km.Completions.Down,
		"completions.select":          &km.Completions.Select,
		"completions.cancel":          &km.Completions.Cancel,
		"completions.insert_next":     &km.Completions.DownInsert,
		"completions.insert_previous": &km.Completions.UpInsert,
	}
}

// ActionNames returns every action name options.tui.keybinds accepts, in
// sorted order. The generated config schema enumerates these, so an action
// exists for the editor the moment it exists for the keymap.
func ActionNames() []string {
	km := DefaultKeyMap()
	return slices.Sorted(maps.Keys(km.keybindActions()))
}

// ApplyKeybinds applies user key overrides from options.tui.keybinds onto
// the keymap. Overrides merge over the defaults: only the named actions are
// rebound, everything else keeps its default keys. Unknown action names and
// empty key lists are warned about and ignored, so a bad entry never blocks
// startup.
func (km *KeyMap) ApplyKeybinds(overrides map[string][]string) {
	if len(overrides) == 0 {
		return
	}
	actions := km.keybindActions()
	for _, action := range slices.Sorted(maps.Keys(overrides)) {
		binding, ok := actions[action]
		if !ok {
			slog.Warn("Ignoring unknown keybind action", "action", action)
			continue
		}
		keys := overrides[action]
		if len(keys) == 0 {
			slog.Warn("Ignoring keybind override with no keys", "action", action)
			continue
		}
		*binding = rebind(*binding, keys)
	}
}

// rebind returns a copy of b bound to keys instead of its current ones. The
// help description survives; the help key text follows the new keys. Every
// binding in the keymap carries help text (enforced by
// TestDefaultKeyMap_HasNoUnannotatedBindings), so a rebound action keeps a
// usable hint.
func rebind(b key.Binding, keys []string) key.Binding {
	return key.NewBinding(
		key.WithKeys(keys...),
		key.WithHelp(strings.Join(keys, "/"), b.Help().Desc),
	)
}

// HelpKeys derives a binding's help key text from the keys it is bound to,
// so hints built from it cannot drift from the keymap or ignore keybind
// overrides. Hand-written key text at a SetHelp call site can do both.
func HelpKeys(b key.Binding) string {
	return strings.Join(b.Keys(), "/")
}

// active is the process-wide keymap. Key bindings are read from every
// layer of the UI — the model, each dialog, the completions popup, the
// chat items — and several of those are constructed without a handle on
// the config, so the merged keymap is installed once at startup and read
// from here rather than threaded through every constructor.
var active atomic.Pointer[KeyMap]

// builtin is the default keymap, built once and shared. Callers treat it
// as read-only.
var builtin = sync.OnceValue(func() *KeyMap {
	km := DefaultKeyMap()
	return &km
})

// Active returns the keymap in force: the one [Install] built from the
// user's options.tui.keybinds, or the built-in defaults before that.
func Active() *KeyMap {
	if km := active.Load(); km != nil {
		return km
	}
	return builtin()
}

// Install merges the user's key overrides over the defaults and makes the
// result the active keymap. Called once, from the UI entry point; the
// returned keymap is the same one [Active] serves afterwards.
func Install(overrides map[string][]string) *KeyMap {
	km := DefaultKeyMap()
	km.ApplyKeybinds(overrides)
	active.Store(&km)
	return &km
}

// ActionTable returns every action name with the keys it is bound to by
// default, sorted by name. The config docs list the same rows, and a test
// compares them, so the table cannot drift from the keymap.
func ActionTable() [][2]string {
	km := DefaultKeyMap()
	actions := km.keybindActions()
	rows := make([][2]string, 0, len(actions))
	for _, name := range slices.Sorted(maps.Keys(actions)) {
		rows = append(rows, [2]string{name, strings.Join(actions[name].Keys(), ", ")})
	}
	return rows
}
