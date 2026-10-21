package model

import "strings"

func (m *UI) shellPrefixes() []string {
	return prefixKeys(m.keyMap.Editor.ShellMode)
}

func (m *UI) shellPrefix() string {
	if prefixes := m.shellPrefixes(); len(prefixes) > 0 {
		return prefixes[0]
	}
	return ""
}

func (m *UI) cutShellPrefix(s string) (string, bool) {
	for _, prefix := range m.shellPrefixes() {
		if rest, ok := strings.CutPrefix(s, prefix); ok {
			return rest, true
		}
	}
	return s, false
}

func (m *UI) setShellMode(on bool) {
	m.bangMode = on
	m.bangWasEmpty = on && m.textarea.Value() == ""
	m.setEditorPrompt()
}
