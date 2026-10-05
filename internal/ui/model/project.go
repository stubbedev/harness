package model

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/stubbedev/harness/internal/ui/util"
)

// initializeProject starts project initialization: it clears the session
// and sends the project initialization prompt as the next message.
func (m *UI) initializeProject() tea.Cmd {
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
