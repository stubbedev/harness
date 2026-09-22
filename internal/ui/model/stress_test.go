package model

import (
	"image"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/chat"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// busyTurnShellTool builds a shell tool item like the chat tests do.
func busyTurnShellTool(sty *styles.Styles, id, command string, finished bool) chat.ToolMessageItem {
	return chat.NewToolMessageItem(sty, "msg", message.ToolCall{
		ID: id, Name: "shell",
		Input:    `{"command":"` + command + `"}`,
		Finished: finished,
	}, nil, false, "/tmp")
}

// TestBusyTurnRenderSurvivesStress drives a full busy turn through the
// real Update/Draw loop looking for the render-path panic seen in the
// wild (TUI killed by a panic mid-turn, no stack in the log): pasted
// inline tokens, spinning tool groups, a picker opened and cancelled,
// and a resize on every frame.
func TestBusyTurnRenderSurvivesStress(t *testing.T) {
	t.Parallel()

	u := newFrameTestUI(t)
	u.com.Workspace = &testWorkspace{cfg: &config.Config{Options: &config.Options{}}}
	u.state = uiChat
	u.session = &session.Session{ID: "s1"}
	u.focus = uiFocusEditor
	u.updateLayoutAndSize()

	// An inline paste token rides the draft.
	u.insertPastedAttachment(imagePaste("paste_1.png"))

	// A live tool group mid-turn: one done call, one still running.
	group := chat.NewToolGroupMessageItem(u.com.Styles, busyTurnShellTool(u.com.Styles, "t1", "ls", true))
	group.AddTool(busyTurnShellTool(u.com.Styles, "t2", "npm test", false))
	u.chat.SetMessages(group)

	// The busy agent keeps the task strip alive.
	warmCaches(u, true)

	// Type into the editor, open and cancel the mention picker, then
	// draw across a range of sizes like a resize storm would.
	for i, r := range "look at [Image #1] " {
		_, _ = u.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		if i%3 == 0 {
			_, _ = u.Update(tea.KeyPressMsg{Code: '@'})
			_, _ = u.Update(tea.KeyPressMsg{Code: 'm', Text: "m"})
			_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		}
		u.width, u.height = 120-(i%7), 40-(i%5)
		u.updateLayoutAndSize()
		u.Draw(uv.NewScreenBuffer(u.width, u.height), image.Rect(0, 0, u.width, u.height))
	}

	require.Contains(t, u.textarea.Value(), "[Image #1]")
}
