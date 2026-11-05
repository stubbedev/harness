package model

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	mcptools "github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/history"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/question"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/dialog"
)

// hangingWorkspace answers the calls a user action waits on only once
// released, the way a client whose server stopped answering does. Each
// call is counted before it blocks, so a test sees one made on the
// Update goroutine even before the hang would trip its deadline.
type hangingWorkspace struct {
	*countingWorkspace
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func newHangingWorkspace(t *testing.T) *hangingWorkspace {
	t.Helper()
	w := &hangingWorkspace{countingWorkspace: &countingWorkspace{ready: true}, release: make(chan struct{})}
	t.Cleanup(w.unblock)
	return w
}

func (w *hangingWorkspace) unblock() { w.once.Do(func() { close(w.release) }) }

func (w *hangingWorkspace) hang() {
	w.calls.Add(1)
	<-w.release
}

func (w *hangingWorkspace) AgentReadyErr(context.Context) error { w.hang(); return nil }

func (w *hangingWorkspace) ListMessages(context.Context, string) ([]message.Message, error) {
	w.hang()
	return nil, nil
}

func (w *hangingWorkspace) CreateSession(context.Context, string) (session.Session, error) {
	w.hang()
	return session.Session{ID: "created"}, nil
}

func (w *hangingWorkspace) ListSessions(context.Context) ([]session.Session, error) {
	w.hang()
	return nil, nil
}

func (w *hangingWorkspace) ListUserMessages(context.Context, string) ([]message.Message, error) {
	w.hang()
	return nil, nil
}

func (w *hangingWorkspace) QuestionAnswer(context.Context, []question.Answer) bool {
	w.hang()
	return true
}

func (w *hangingWorkspace) QuestionCancel(context.Context) bool { w.hang(); return true }

func (w *hangingWorkspace) MCPPendingAuth(context.Context) []mcptools.PendingAuthServer {
	w.hang()
	return nil
}

// requireUpdateReturns drives msg through Update and fails unless it
// returns without making any of the hanging calls.
func requireUpdateReturns(t *testing.T, m *UI, ws *hangingWorkspace, msg tea.Msg) tea.Cmd {
	t.Helper()
	return requireReturns(t, ws, func() tea.Cmd {
		_, cmd := m.Update(msg)
		return cmd
	})
}

// requireReturns runs handle, a step of Update, and fails unless it
// returns without making any of the hanging calls.
func requireReturns(t *testing.T, ws *hangingWorkspace, handle func() tea.Cmd) tea.Cmd {
	t.Helper()
	cmd := returnsPromptly(t, handle)
	require.Zero(t, ws.calls.Load(), "Update must not call the workspace")
	return cmd
}

func newHangingUI(t *testing.T) (*UI, *hangingWorkspace) {
	t.Helper()
	ws := newHangingWorkspace(t)
	m := newBusyUI(ws.countingWorkspace)
	m.com = common.DefaultCommon(ws)
	warmCaches(m, false)
	return m, ws
}

// TestUpdateNeverWaitsOnTheWorkspace pins that a user action whose
// workspace call is a server round trip in client/server mode never makes
// it on the Update goroutine: a slow or dead server must not freeze the
// TUI on enter, a session switch, a first prompt or a question answer.
func TestUpdateNeverWaitsOnTheWorkspace(t *testing.T) {
	t.Parallel()

	t.Run("enter", func(t *testing.T) {
		t.Parallel()
		m, ws := newHangingUI(t)
		m.textarea.SetValue("hello")
		cmd := requireUpdateReturns(t, m, ws, tea.KeyPressMsg{Code: tea.KeyEnter})
		require.NotNil(t, cmd, "the readiness check runs as a command")
		require.Equal(t, "hello", m.textarea.Value(), "the draft waits for the check")
	})

	t.Run("session load", func(t *testing.T) {
		t.Parallel()
		m, ws := newHangingUI(t)
		m.sessionLoadRequested = "s2"
		requireUpdateReturns(t, m, ws, loadSessionMsg{session: &session.Session{ID: "s2"}})
		require.Equal(t, "s2", m.currentSessionID())
	})

	t.Run("prompt from a command", func(t *testing.T) {
		t.Parallel()
		m, ws := newHangingUI(t)
		require.NotNil(t, requireUpdateReturns(t, m, ws, sendMessageMsg{Content: "from a skill"}))
	})

	t.Run("first prompt creates its session off-thread", func(t *testing.T) {
		t.Parallel()
		m, ws := newHangingUI(t)
		m.session = nil
		cmd := requireUpdateReturns(t, m, ws, promptReadyMsg{content: "first"})
		require.NotNil(t, cmd, "the session is created by a command")
		require.False(t, m.hasSession(), "no session exists until the creation lands")
	})

	t.Run("bang command creates its session off-thread", func(t *testing.T) {
		t.Parallel()
		m, ws := newHangingUI(t)
		m.session = nil
		m.bangMode = true
		m.textarea.SetValue("ls")
		require.NotNil(t, requireUpdateReturns(t, m, ws, tea.KeyPressMsg{Code: tea.KeyEnter}))
		require.False(t, m.hasSession())
	})

	t.Run("question cancel", func(t *testing.T) {
		t.Parallel()
		m, ws := newHangingUI(t)
		m.openBatchFormDialog(question.Request{ID: "b", Questions: []question.Question{
			{ID: "q1", Type: question.TypeFreeText, Text: "Why?"},
		}})
		cmd := requireUpdateReturns(t, m, ws, tea.KeyPressMsg{Code: tea.KeyEscape})
		require.Nil(t, m.activeInline, "the form closes at once")
		require.NotNil(t, cmd)
		ws.unblock()
		runAll(cmd)
		require.Equal(t, int32(1), ws.calls.Load(), "the cancel reaches the workspace from the command")
	})

	t.Run("sessions dialog", func(t *testing.T) {
		t.Parallel()
		m, ws := newHangingUI(t)
		require.NotNil(t, requireReturns(t, ws, m.openSessionsDialog), "the list is fetched by a command")
		require.True(t, m.dialog.ContainsDialog(dialog.SessionsID), "the dialog opens before its list lands")
	})

	t.Run("rewind dialog", func(t *testing.T) {
		t.Parallel()
		m, ws := newHangingUI(t)
		require.NotNil(t, requireReturns(t, ws, m.openRewindDialog), "the turns are fetched by a command")
		require.True(t, m.dialog.ContainsDialog(dialog.RewindID), "the dialog opens before its turns land")
	})

	t.Run("mcp auth dialog", func(t *testing.T) {
		t.Parallel()
		m, ws := newHangingUI(t)
		require.NotNil(t, requireReturns(t, ws, func() tea.Cmd { return m.handleAction(dialog.ActionOpenMCPAuth{}) }))
	})
}

// runAll executes every leaf of a command tree for its side effects.
func runAll(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			runAll(c)
		}
	}
}

// sessionRecordingWorkspace records the session every prompt was sent to.
type sessionRecordingWorkspace struct {
	*countingWorkspace
	created  int
	sentTo   []string
	creating string
}

func (w *sessionRecordingWorkspace) CreateSession(context.Context, string) (session.Session, error) {
	w.created++
	return session.Session{ID: w.creating}, nil
}

func (w *sessionRecordingWorkspace) AgentRun(_ context.Context, sessionID, _ string, _ ...message.Attachment) error {
	w.sentTo = append(w.sentTo, sessionID)
	return nil
}

func (w *sessionRecordingWorkspace) GetSession(_ context.Context, id string) (session.Session, error) {
	return session.Session{ID: id}, nil
}

func (w *sessionRecordingWorkspace) ListSessionHistory(context.Context, string) ([]history.File, error) {
	return nil, nil
}

func (w *sessionRecordingWorkspace) FileTrackerListReadFiles(context.Context, string) ([]string, error) {
	return nil, nil
}

func (w *sessionRecordingWorkspace) SetCurrentSession(context.Context, string) error { return nil }

// TestPromptsBeforeTheirSessionExistsLandInIt pins the ordering the
// off-thread creation keeps: prompts sent while no session is open wait
// for the one session created, and go to it, in order.
func TestPromptsBeforeTheirSessionExistsLandInIt(t *testing.T) {
	t.Parallel()

	ws := &sessionRecordingWorkspace{countingWorkspace: &countingWorkspace{ready: true}, creating: "fresh"}
	m := newBusyUI(ws.countingWorkspace)
	m.com = common.DefaultCommon(ws)
	warmCaches(m, false)
	m.session = nil

	create := m.sendReadyMessage("first")
	require.NotNil(t, create)
	require.Nil(t, m.sendReadyMessage("second"), "a creation in flight is not started twice")

	created, ok := create().(newSessionCreatedMsg)
	require.True(t, ok)
	_, cmd := m.Update(created)
	runAll(cmd)

	require.Equal(t, 1, ws.created)
	require.Equal(t, "fresh", m.currentSessionID())
	require.Equal(t, []string{"fresh", "fresh"}, ws.sentTo)
}

// TestSubmitCheckRacingASwitchKeepsTheDraft pins the race guard on the
// editor's readiness check: a result for a session the user has left
// sends nothing and leaves the draft where it was.
func TestSubmitCheckRacingASwitchKeepsTheDraft(t *testing.T) {
	t.Parallel()

	m, ws := newRecordingUI(t)
	m.textarea.SetValue("hello")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	checked := findMsg[submitCheckedMsg](t, drainMsgs(cmd))

	m.session = &session.Session{ID: "elsewhere"}
	m.Update(checked)
	require.Empty(t, ws.runs)
	require.Equal(t, "hello", m.textarea.Value())
	require.False(t, m.submitPending, "a dropped check frees the next submit")
}
