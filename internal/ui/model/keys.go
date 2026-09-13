package model

import (
	"log/slog"
	"maps"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
)

type KeyMap struct {
	Editor struct {
		SendMessage key.Binding
		OpenEditor  key.Binding
		Newline     key.Binding
		AddImage    key.Binding
		PasteImage  key.Binding
		MentionFile key.Binding
		Commands    key.Binding

		// Attachments key maps
		AttachmentDeleteMode key.Binding
		Escape               key.Binding
		DeleteAllAttachments key.Binding

		// History navigation
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

		// PasteText pastes clipboard text into the textarea, as an
		// alternative to bracketed paste.
		PasteText key.Binding
	}

	Chat struct {
		NewSession      key.Binding
		AddAttachment   key.Binding
		Cancel          key.Binding
		Tab             key.Binding
		Details         key.Binding
		TogglePills     key.Binding
		PillLeft        key.Binding
		PillRight       key.Binding
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

	// Global key maps
	Quit          key.Binding
	Help          key.Binding
	Commands      key.Binding
	Models        key.Binding
	Suspend       key.Binding
	Sessions      key.Binding
	Tab           key.Binding
	ParentSession key.Binding
	// ExportConversation writes the current session transcript to a
	// markdown file and copies it to the clipboard.
	ExportConversation key.Binding
	// Themes opens the color theme picker.
	Themes key.Binding
}

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
		// alt+t.
		Themes: key.NewBinding(
			key.WithKeys("alt+t"),
			key.WithHelp("alt+t", "themes"),
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
		// "ctrl+j" is a common keybinding for newline in many editors. If
		// the terminal supports "shift+enter", we substitute the help tex
		// to reflect that.
		key.WithHelp("ctrl+j", "newline"),
	)
	km.Editor.AddImage = key.NewBinding(
		key.WithKeys("ctrl+f"),
		key.WithHelp("ctrl+f", "add image"),
	)
	km.Editor.PasteImage = key.NewBinding(
		key.WithKeys("ctrl+v"),
		key.WithHelp("ctrl+v", "paste image from clipboard"),
	)
	km.Editor.PasteText = key.NewBinding(
		key.WithKeys("ctrl+shift+v"),
		key.WithHelp("ctrl+shift+v", "paste text"),
	)
	km.Editor.MentionFile = key.NewBinding(
		key.WithKeys("@"),
		key.WithHelp("@", "mention file"),
	)
	km.Editor.Commands = key.NewBinding(
		key.WithKeys("/"),
		key.WithHelp("/", "commands"),
	)
	km.Editor.AttachmentDeleteMode = key.NewBinding(
		key.WithKeys("ctrl+r"),
		key.WithHelp("ctrl+r+{i}", "delete attachment at index i"),
	)
	km.Editor.Escape = key.NewBinding(
		key.WithKeys("esc", "alt+esc"),
		key.WithHelp("esc", "cancel delete mode"),
	)
	km.Editor.DeleteAllAttachments = key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("ctrl+r+r", "delete all attachments"),
	)
	km.Editor.HistoryPrev = key.NewBinding(
		key.WithKeys("up"),
	)
	km.Editor.HistoryNext = key.NewBinding(
		key.WithKeys("down"),
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

	km.Chat.NewSession = key.NewBinding(
		key.WithKeys("ctrl+n"),
		key.WithHelp("ctrl+n", "new session"),
	)
	km.Chat.AddAttachment = key.NewBinding(
		key.WithKeys("ctrl+f"),
		key.WithHelp("ctrl+f", "add attachment"),
	)
	km.Chat.Cancel = key.NewBinding(
		key.WithKeys("esc", "alt+esc"),
		key.WithHelp("esc", "cancel"),
	)
	km.Chat.Tab = key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "change focus"),
	)
	km.Chat.Details = key.NewBinding(
		key.WithKeys("ctrl+d"),
		key.WithHelp("ctrl+d", "toggle details"),
	)
	km.Chat.TogglePills = key.NewBinding(
		key.WithKeys("ctrl+t", "ctrl+space"),
		key.WithHelp("ctrl+t", "toggle tasks"),
	)
	km.Chat.PillLeft = key.NewBinding(
		key.WithKeys("left"),
		key.WithHelp("←/→", "switch section"),
	)
	km.Chat.PillRight = key.NewBinding(
		key.WithKeys("right"),
		key.WithHelp("←/→", "switch section"),
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
	)
	km.Chat.Copy = key.NewBinding(
		key.WithKeys("c", "y", "C", "Y"),
		key.WithHelp("c/y", "copy"),
	)
	km.Chat.ClearHighlight = key.NewBinding(
		key.WithKeys("esc", "alt+esc"),
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

	return km
}

// keybindActions maps the stable action names accepted by
// options.tui.keybinds to the bindings they override. Names mirror the
// KeyMap fields: the global group is bare, the editor group carries the
// "editor." prefix, and the chat group the "chat." prefix.
func (km *KeyMap) keybindActions() map[string]*key.Binding {
	return map[string]*key.Binding{
		"quit":                          &km.Quit,
		"help":                          &km.Help,
		"commands":                      &km.Commands,
		"models":                        &km.Models,
		"suspend":                       &km.Suspend,
		"sessions":                      &km.Sessions,
		"tab":                           &km.Tab,
		"parent_session":                &km.ParentSession,
		"export_conversation":           &km.ExportConversation,
		"themes":                        &km.Themes,
		"editor.send_message":           &km.Editor.SendMessage,
		"editor.open_editor":            &km.Editor.OpenEditor,
		"editor.newline":                &km.Editor.Newline,
		"editor.add_image":              &km.Editor.AddImage,
		"editor.paste_image":            &km.Editor.PasteImage,
		"editor.paste_text":             &km.Editor.PasteText,
		"editor.mention_file":           &km.Editor.MentionFile,
		"editor.commands":               &km.Editor.Commands,
		"editor.attachment_delete_mode": &km.Editor.AttachmentDeleteMode,
		"editor.escape":                 &km.Editor.Escape,
		"editor.delete_all_attachments": &km.Editor.DeleteAllAttachments,
		"editor.history_prev":           &km.Editor.HistoryPrev,
		"editor.history_next":           &km.Editor.HistoryNext,
		"editor.copy_selection":         &km.Editor.CopySelection,
		"editor.cut_selection":          &km.Editor.CutSelection,
		"editor.select_all":             &km.Editor.SelectAll,
		"chat.new_session":              &km.Chat.NewSession,
		"chat.add_attachment":           &km.Chat.AddAttachment,
		"chat.cancel":                   &km.Chat.Cancel,
		"chat.tab":                      &km.Chat.Tab,
		"chat.details":                  &km.Chat.Details,
		"chat.toggle_pills":             &km.Chat.TogglePills,
		"chat.pill_left":                &km.Chat.PillLeft,
		"chat.pill_right":               &km.Chat.PillRight,
		"chat.down":                     &km.Chat.Down,
		"chat.up":                       &km.Chat.Up,
		"chat.up_down":                  &km.Chat.UpDown,
		"chat.down_one_item":            &km.Chat.DownOneItem,
		"chat.up_one_item":              &km.Chat.UpOneItem,
		"chat.up_down_one_item":         &km.Chat.UpDownOneItem,
		"chat.page_down":                &km.Chat.PageDown,
		"chat.page_up":                  &km.Chat.PageUp,
		"chat.half_page_down":           &km.Chat.HalfPageDown,
		"chat.half_page_up":             &km.Chat.HalfPageUp,
		"chat.home":                     &km.Chat.Home,
		"chat.end":                      &km.Chat.End,
		"chat.end_follow":               &km.Chat.EndFollow,
		"chat.copy":                     &km.Chat.Copy,
		"chat.clear_highlight":          &km.Chat.ClearHighlight,
		"chat.expand":                   &km.Chat.Expand,
		"chat.dig_in":                   &km.Chat.DigIn,
		"chat.scroll_left":              &km.Chat.ScrollLeft,
		"chat.scroll_right":             &km.Chat.ScrollRight,
		"chat.background_tasks":         &km.Chat.BackgroundTasks,
	}
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
// help description survives; the help key text follows the new keys.
// Bindings without help text (chat.end_follow, the history bindings) stay
// help-less.
func rebind(b key.Binding, keys []string) key.Binding {
	opts := []key.BindingOpt{key.WithKeys(keys...)}
	if help := b.Help(); help.Desc != "" || help.Key != "" {
		opts = append(opts, key.WithHelp(strings.Join(keys, "/"), help.Desc))
	}
	return key.NewBinding(opts...)
}
