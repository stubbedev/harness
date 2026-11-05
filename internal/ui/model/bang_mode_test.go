package model

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/proto"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/chat"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/dialog"
)

// bangWorkspace records shell commands the UI issues through bang mode.
type bangWorkspace struct {
	*countingWorkspace

	mu       sync.Mutex
	commands []string
}

func (w *bangWorkspace) AgentRunShellCommand(ctx context.Context, sessionID, command string, termWidth int, onProgress func(string), isFirstMessage bool) (proto.ShellCommandResponse, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.commands = append(w.commands, command)
	if onProgress != nil {
		onProgress("ok\n")
	}
	return proto.ShellCommandResponse{Output: "ok", ExitCode: 0}, nil
}

func newBangUI() (*UI, *bangWorkspace) {
	ws := &bangWorkspace{countingWorkspace: &countingWorkspace{ready: true}}
	com := common.DefaultCommon(ws)
	m := &UI{
		com:      com,
		status:   NewStatus(com, nil),
		chat:     NewChat(com, config.ScrollbarDefault),
		textarea: textarea.New(),
		state:    uiChat,
		focus:    uiFocusEditor,
		width:    140,
		height:   45,
		session:  &session.Session{ID: "s1"},
		keyMap:   DefaultKeyMap(),
		dialog:   dialog.NewOverlay(),
	}
	// Production always has the prompt focused when the user types; the
	// textarea drops keys while blurred, which would silently skip the
	// whole bang path under test.
	m.textarea.Focus()
	return m, ws
}

// runBangUpdate executes the command Update returned the way the
// runtime does: batches recurse, and every inner command runs.
func runBangUpdate(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return
	}
	var wg sync.WaitGroup
	for _, c := range batch {
		wg.Add(1)
		go func(c tea.Cmd) {
			defer wg.Done()
			runBangUpdate(t, c)
		}(c)
	}
	wg.Wait()
}

// TestBangModeRunsShellCommand drives the real key path: typing "!"
// enters bang mode and strips the bang, and Enter executes the command
// through the workspace without going through the agent.
func TestBangModeRunsShellCommand(t *testing.T) {
	t.Parallel()

	m, ws := newBangUI()
	for _, r := range "!echo hi" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	require.True(t, m.bangMode, "typing ! enters bang mode")
	require.Equal(t, "echo hi", m.textarea.Value(), "the bang is stripped from the prompt")

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.NotNil(t, cmd, "Enter must produce a command")

	done := make(chan struct{})
	go func() {
		defer close(done)
		runBangUpdate(t, cmd)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("bang execution deadlocked")
	}

	ws.mu.Lock()
	defer ws.mu.Unlock()
	require.Equal(t, []string{"echo hi"}, ws.commands, "Enter must run the command through the workspace")
	require.False(t, m.bangMode, "bang mode exits after the command is issued")
}

// TestBangResultStaysInItsSession is the cross-session regression: a
// bang command started in one session and still running when the user
// switches to another must neither keep the new session busy, nor be
// stopped by its Escape, nor paint its output into its transcript.
func TestBangResultStaysInItsSession(t *testing.T) {
	t.Parallel()

	m, _ := newBangUI()
	m.runShellCommand("make test")
	require.Equal(t, 1, m.chat.Len(), "the pending item shows at once")
	pending, ok := m.chat.list.ItemAt(0).(*chat.ShellItem)
	require.True(t, ok)
	require.True(t, m.isAgentBusy(), "the session running the command is busy")

	m.session = &session.Session{ID: "s2"}
	m.chat.SetMessages("s2")

	require.False(t, m.isAgentBusy(), "another session's command does not make this one busy")
	m.cancelBangRuns()
	require.Len(t, m.bangRuns, 1, "stopping this session leaves the other's command running")

	m.Update(shellResultMsg{PendingID: pending.ID(), SessionID: "s1", Command: "make test", Output: "PASS"})

	require.Zero(t, m.chat.Len(), "the result does not paint into another session's transcript")
	require.Empty(t, m.bangRuns, "the finished command is forgotten")
}

// TestBangResultAppendsInItsOwnSession keeps the fallback: a result whose
// pending item was dropped by a reload of the same session still shows.
func TestBangResultAppendsInItsOwnSession(t *testing.T) {
	t.Parallel()

	m, _ := newBangUI()
	m.Update(shellResultMsg{PendingID: "gone", SessionID: "s1", Command: "make test", Output: "PASS"})

	require.Equal(t, 1, m.chat.Len())
	_, ok := m.chat.list.ItemAt(0).(*chat.ShellItem)
	require.True(t, ok)
}

// TestBangCommandThatCannotRunSettlesItsRow pins the error path: a
// command that never ran still ends its pending row, showing why,
// instead of leaving it spinning.
func TestBangCommandThatCannotRunSettlesItsRow(t *testing.T) {
	t.Parallel()

	m, _ := newBangUI()
	m.runShellCommand("make test")
	pending, ok := m.chat.list.ItemAt(0).(*chat.ShellItem)
	require.True(t, ok)

	m.Update(shellResultMsg{PendingID: pending.ID(), SessionID: "s1", Command: "make test", Err: errors.New("no shell available")})

	require.True(t, pending.Finished(), "the row stops spinning")
	require.False(t, pending.Spinning())
	rendered := ansi.Strip(pending.Render(80))
	require.Contains(t, rendered, "failed to run")
	require.Contains(t, rendered, "no shell available")
	require.Empty(t, m.bangRuns)
}
