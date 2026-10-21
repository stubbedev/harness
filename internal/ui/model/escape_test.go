package model

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/pubsub"
	"github.com/stubbedev/harness/internal/ui/dialog"
	"github.com/stubbedev/harness/internal/workspace"
)

func pressEsc(m *UI) tea.Cmd {
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	return cmd
}

func escHint(t *testing.T, m *UI) string {
	t.Helper()
	b, ok := m.escHintBinding()
	if !ok {
		return ""
	}
	return b.Help().Desc
}

func TestEscapeTwiceStopsEverythingAndRestoresQueue(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, agentBusy: true, queued: []string{"first", "second"}}
	m := newBusyUI(ws)
	warmCaches(m, true)
	m.promptQueueItems = []string{"first", "second"}
	m.appendQueuedPrompt("first")
	m.appendQueuedPrompt("second")
	require.NotNil(t, m.queuedPromptItem)
	var bangCanceled bool
	m.bangCancel = func() { bangCanceled = true }

	require.Equal(t, "press twice to stop", escHint(t, m))
	runCmds(m, pressEsc(m))
	require.Equal(t, 1, m.esc.state)
	require.Zero(t, ws.cancelCalls, "the first press stops nothing")
	require.True(t, m.isAgentBusy())
	require.Equal(t, "press again to stop", escHint(t, m))

	runCmds(m, pressEsc(m))
	require.Equal(t, 1, ws.cancelCalls, "the second press stops the whole session")
	require.False(t, m.isAgentBusy(), "the UI is idle as soon as the stop is pressed")
	require.True(t, bangCanceled, "a running bang command is canceled")
	require.Nil(t, m.bangCancel)
	require.Empty(t, m.promptQueueItems)
	require.Empty(t, m.queuedPrompts)
	require.Nil(t, m.queuedPromptItem, "the queued placeholder leaves the transcript")
	require.Equal(t, "first\n\nsecond", m.textarea.Value(), "the dropped prompts return to the editor")
	require.Equal(t, "press again to rewind", escHint(t, m))
}

func TestEscapeStopAppendsQueuedPromptsAfterDraft(t *testing.T) {
	pinTTLs(t)

	m := newBusyUI(&countingWorkspace{ready: true, agentBusy: true})
	warmCaches(m, true)
	m.promptQueueItems = []string{"queued"}
	m.textarea.InsertString("draft")

	runCmds(m, pressEsc(m))
	runCmds(m, pressEsc(m))

	require.Equal(t, "draft\n\nqueued", m.textarea.Value(), "nothing typed is lost")
}

func TestEscapeStopsRunningSubagentsWhileIdle(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false)
	m.runningSubagents = []workspace.RunningSubagentInfo{{}}

	runCmds(m, pressEsc(m))
	runCmds(m, pressEsc(m))

	require.Equal(t, 1, ws.cancelCalls, "background subagents count as something to stop")
}

func TestEscapeThirdPressOpensRewindAfterStop(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, agentBusy: true}
	m := newBusyUI(ws)
	warmCaches(m, true)

	runCmds(m, pressEsc(m))
	runCmds(m, pressEsc(m))
	require.False(t, m.dialog.ContainsDialog(dialog.RewindID))
	runCmds(m, pressEsc(m))

	require.Equal(t, 1, ws.cancelCalls)
	require.True(t, m.dialog.ContainsDialog(dialog.RewindID), "the third press opens the rewind picker")
	require.Zero(t, m.esc.state)
}

func TestIdleEscapeNeedsThreePressesToRewind(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false)

	require.Empty(t, escHint(t, m), "an idle editor hints nothing before a press")
	runCmds(m, pressEsc(m))
	require.Equal(t, "press twice more to rewind", escHint(t, m))
	runCmds(m, pressEsc(m))
	require.False(t, m.dialog.ContainsDialog(dialog.RewindID), "two idle presses do not rewind")
	require.Zero(t, ws.cancelCalls, "two idle presses have nothing to stop")
	require.False(t, m.stopping)
	require.Equal(t, "press again to rewind", escHint(t, m))
	runCmds(m, pressEsc(m))
	require.True(t, m.dialog.ContainsDialog(dialog.RewindID))
}

func TestEscapeWindowExpiryResetsCount(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, agentBusy: true}
	m := newBusyUI(ws)
	warmCaches(m, true)

	runCmds(m, pressEsc(m))
	stale := m.esc.gen
	runCmds(m, pressEsc(m))
	m.Update(escWindowExpiredMsg{gen: stale})
	require.Equal(t, 2, m.esc.state, "an earlier press's timer never resets a newer one")

	m.Update(escWindowExpiredMsg{gen: m.esc.gen})
	require.Zero(t, m.esc.state)
	runCmds(m, pressEsc(m))
	require.Equal(t, 1, m.esc.state, "a press after the window starts a new count")
	require.False(t, m.dialog.ContainsDialog(dialog.RewindID))
}

func TestEscapeOwnedByDraftAndHistoryWhenIdle(t *testing.T) {
	pinTTLs(t)

	m := newBusyUI(&countingWorkspace{ready: true})
	warmCaches(m, false)

	m.textarea.InsertString("draft")
	consumed, _ := m.handleEscape()
	require.False(t, consumed, "a draft owns the press")
	require.Zero(t, m.esc.state)
	m.textarea.Reset()

	m.promptHistory.messages = []message.PromptHistoryEntry{{Text: "older prompt"}}
	m.promptHistory.pos = 1
	consumed, _ = m.handleEscape()
	require.False(t, consumed, "history browsing owns the press")
	require.Zero(t, m.esc.state)
	m.promptHistory.pos = 0

	m.focus = uiFocusMain
	consumed, _ = m.handleEscape()
	require.False(t, consumed, "escape counts only from the editor")
	_, ok := m.escHintBinding()
	require.False(t, ok, "and is hinted only there")
}

func TestEscapeStopsDespiteDraftWhileBusy(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, agentBusy: true}
	m := newBusyUI(ws)
	warmCaches(m, true)
	m.textarea.InsertString("draft")

	runCmds(m, pressEsc(m))
	runCmds(m, pressEsc(m))

	require.Equal(t, 1, ws.cancelCalls)
	require.Equal(t, "draft", m.textarea.Value())
}

type blockingCancelWorkspace struct {
	*countingWorkspace
	release chan struct{}
}

func (w *blockingCancelWorkspace) AgentCancel(string) error {
	<-w.release
	return nil
}

func goCmds(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		if batch, ok := cmd().(tea.BatchMsg); ok {
			for _, c := range batch {
				goCmds(c)
			}
		}
	}()
}

func TestStopIsImmediateWhileWorkspaceCancelBlocks(t *testing.T) {
	pinTTLs(t)

	ws := &blockingCancelWorkspace{
		countingWorkspace: &countingWorkspace{ready: true, agentBusy: true},
		release:           make(chan struct{}),
	}
	m := newBusyUI(ws.countingWorkspace)
	m.com.Workspace = ws
	warmCaches(m, true)
	m.promptQueueItems = []string{"queued"}

	pressEsc(m)
	consumed, stopCmd := m.handleEscape()
	require.True(t, consumed)
	require.NotNil(t, stopCmd)
	require.False(t, m.isAgentBusy(), "idle without waiting on the cancel round trip")
	require.True(t, m.stopping)
	require.Equal(t, "queued", m.textarea.Value())
	m.textarea.Reset()
	m.textarea.Focus()
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	require.Contains(t, m.textarea.Value(), "x", "the editor accepts input at once")

	m.busyFetchInFlight = false
	probe := m.dispatchBusyRefresh()
	require.NotNil(t, probe)
	probed := make(chan tea.Msg, 1)
	go func() { probed <- probe() }()
	goCmds(stopCmd)
	select {
	case <-probed:
		t.Fatal("a probe must not read state before the stop lands")
	case <-time.After(50 * time.Millisecond):
	}

	close(ws.release)
	select {
	case msg := <-probed:
		require.IsType(t, busyStateMsg{}, msg)
	case <-time.After(5 * time.Second):
		t.Fatal("the probe must run once the stop lands")
	}
}

func TestLateBusyAfterStopDoesNotReMarkBusy(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, agentBusy: true}
	m := newBusyUI(ws)
	warmCaches(m, true)

	runCmds(m, pressEsc(m))
	runCmds(m, pressEsc(m))
	require.False(t, m.isAgentBusy())

	runCmds(m, m.dispatchBusyRefresh())
	require.False(t, m.isAgentBusy(), "a probe still seeing the run unwind keeps the UI idle")
	require.True(t, m.stopping)

	_, late := m.Update(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: message.Message{ID: "late", SessionID: "s1", Role: message.Assistant}})
	runCmds(m, late)
	runCmds(m, m.dispatchBusyRefresh())
	require.False(t, m.isAgentBusy(), "a late event from the stopped run never flips the UI back to busy")

	ws.agentBusy = false
	m.invalidateBusyCaches()
	runCmds(m, m.dispatchBusyRefresh())
	require.False(t, m.stopping, "the stop settles once the backend reports idle")

	ws.agentBusy = true
	m.invalidateBusyCaches()
	runCmds(m, m.dispatchBusyRefresh())
	require.True(t, m.isAgentBusy(), "a run after the stop settled shows as busy again")
}

func TestSendAfterStopClearsStopGuard(t *testing.T) {
	pinTTLs(t)

	m := newBusyUI(&countingWorkspace{ready: true, agentBusy: true})
	warmCaches(m, true)
	runCmds(m, pressEsc(m))
	runCmds(m, pressEsc(m))
	require.True(t, m.stopping)

	require.NotNil(t, m.sendMessage("next"))
	require.False(t, m.stopping)
	require.True(t, m.isAgentBusy())
}
