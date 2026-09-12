package model

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/stubbedev/harness/internal/ui/util"
)

// initializeProject starts project initialization and transitions to the landing view.
func (m *UI) initializeProject() tea.Cmd {
	// clear the session
	var cmds []tea.Cmd
	if cmd := m.newSession(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	initialize := func() tea.Msg {
		initPrompt, err := m.com.Workspace.InitializePrompt()
		if err != nil {
			return util.InfoMsg{
				Type: util.InfoTypeError,
				Msg:  fmt.Sprintf("Failed to initialize project: %v", err),
			}
		}
		return sendMessageMsg{Content: initPrompt}
	}
	cmds = append(cmds, initialize)

	return tea.Sequence(cmds...)
}
