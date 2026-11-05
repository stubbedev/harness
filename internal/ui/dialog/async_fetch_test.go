package dialog

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"

	mcptools "github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/checkpoints"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/styles"
	"github.com/stubbedev/harness/internal/workspace"
)

// hangingDialogWorkspace answers the calls the dialogs fetch with only
// once released, the way a client whose server stopped answering does.
// Each call is counted before it blocks.
type hangingDialogWorkspace struct {
	workspace.Workspace
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func newHangingDialogWorkspace(t *testing.T) *hangingDialogWorkspace {
	t.Helper()
	w := &hangingDialogWorkspace{release: make(chan struct{})}
	t.Cleanup(func() { w.once.Do(func() { close(w.release) }) })
	return w
}

func (w *hangingDialogWorkspace) hang() {
	w.calls.Add(1)
	<-w.release
}

func (w *hangingDialogWorkspace) ListSessions(context.Context) ([]session.Session, error) {
	w.hang()
	return nil, nil
}

func (w *hangingDialogWorkspace) AgentIsReady(context.Context) bool { w.hang(); return true }

func (w *hangingDialogWorkspace) AgentIsSessionBusy(context.Context, string) (bool, error) {
	w.hang()
	return false, nil
}

func (w *hangingDialogWorkspace) ListUserMessages(context.Context, string) ([]message.Message, error) {
	w.hang()
	return nil, nil
}

func (w *hangingDialogWorkspace) ListCheckpoints(context.Context, string) ([]checkpoints.Checkpoint, error) {
	w.hang()
	return nil, nil
}

func (w *hangingDialogWorkspace) MCPAuthURL(context.Context, string) (string, error) {
	w.hang()
	return "", nil
}

func hangingCommon(t *testing.T, ws workspace.Workspace) *common.Common {
	t.Helper()
	sty := styles.CharmtonePantera()
	return &common.Common{Workspace: ws, Styles: &sty}
}

// requireNoWait runs step, which stands for work on the update loop, and
// fails unless it returns without making any of the hanging calls.
func requireNoWait(t *testing.T, ws *hangingDialogWorkspace, step func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		step()
	}()
	select {
	case <-done:
		require.Zero(t, ws.calls.Load(), "the update loop must not call the workspace")
	case <-time.After(5 * time.Second):
		t.Fatal("the update loop blocked on the workspace")
	}
}

func drawDialog(d Dialog) {
	scr := uv.NewScreenBuffer(100, 40)
	d.Draw(scr, scr.Bounds())
}

// TestDialogsNeverWaitOnTheWorkspace pins that opening, drawing and
// driving the sessions, rewind and MCP auth dialogs makes no workspace
// call on the update loop: each fetch runs in a command, so a slow or
// dead server cannot freeze the TUI.
func TestDialogsNeverWaitOnTheWorkspace(t *testing.T) {
	t.Parallel()

	t.Run("sessions", func(t *testing.T) {
		t.Parallel()
		ws := newHangingDialogWorkspace(t)
		requireNoWait(t, ws, func() {
			s, load := NewSessions(hangingCommon(t, ws), "s1")
			require.NotNil(t, load)
			drawDialog(s)
			require.Equal(t, "Loading sessions…", s.loadStatus())
			s.HandleMsg(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
		})
	})

	t.Run("session delete check", func(t *testing.T) {
		t.Parallel()
		ws := newHangingDialogWorkspace(t)
		requireNoWait(t, ws, func() {
			s, _ := NewSessions(hangingCommon(t, ws), "s1")
			s.HandleMsg(sessionsLoadedMsg{owner: s, sessions: []session.Session{{ID: "s1", Title: "One"}}})
			action, ok := s.HandleMsg(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}).(ActionCmd)
			require.True(t, ok, "the busy check is a command")
			require.NotNil(t, action.Cmd)
			require.Equal(t, sessionsModeNormal, s.sessionsMode, "delete waits for the check")
		})
	})

	t.Run("rewind", func(t *testing.T) {
		t.Parallel()
		ws := newHangingDialogWorkspace(t)
		requireNoWait(t, ws, func() {
			r, load := NewRewind(hangingCommon(t, ws), "s1")
			require.NotNil(t, load)
			drawDialog(r)
			require.True(t, r.loading)
		})
	})

	t.Run("mcp auth", func(t *testing.T) {
		t.Parallel()
		ws := newHangingDialogWorkspace(t)
		requireNoWait(t, ws, func() {
			m, _ := NewMCPAuth(hangingCommon(t, ws), []mcptools.PendingAuthServer{{Name: "srv", URL: "https://example.com"}}, ws.MCPAuthURL)
			m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
			require.Equal(t, MCPAuthStateAuthenticating, m.state)
			drawDialog(m)
			m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
			m.HandleMsg(tea.KeyPressMsg{Code: 'c'})
			m.CancelAuth()
		})
	})
}

// TestDialogFetchForAnotherInstanceIsDropped pins the staleness guard: a
// result fetched for a dialog that was closed, or closed and reopened,
// does not land in the dialog open now.
func TestDialogFetchForAnotherInstanceIsDropped(t *testing.T) {
	t.Parallel()
	com := hangingCommon(t, newHangingDialogWorkspace(t))

	closed, _ := NewSessions(com, "")
	reopened, _ := NewSessions(com, "")
	reopened.HandleMsg(sessionsLoadedMsg{owner: closed, sessions: []session.Session{{ID: "s1"}}})
	require.True(t, reopened.loading, "a list fetched for another dialog is dropped")
	require.Empty(t, reopened.sessions)

	oldRewind, _ := NewRewind(com, "s1")
	rewind, _ := NewRewind(com, "s1")
	rewind.HandleMsg(rewindLoadedMsg{owner: oldRewind, turns: []RewindTurn{{MessageID: "m1", Prompt: "p"}}})
	require.True(t, rewind.loading)
	require.Empty(t, rewind.turns)

	oldAuth, _ := NewMCPAuth(com, []mcptools.PendingAuthServer{{Name: "srv"}}, nil)
	auth, _ := NewMCPAuth(com, []mcptools.PendingAuthServer{{Name: "srv"}}, nil)
	auth.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	auth.HandleMsg(mcpAuthURLMsg{owner: oldAuth, server: "srv", url: "https://stale"})
	require.Empty(t, auth.authURL)
	auth.HandleMsg(mcpAuthURLMsg{owner: auth, server: "srv", url: "https://fresh"})
	require.Equal(t, "https://fresh", auth.authURL)
	auth.CancelAuth()
}

// TestOverlayRoutesFetchToItsDialog pins that a fetch result reaches the
// dialog that asked for it even when another dialog was opened over it.
func TestOverlayRoutesFetchToItsDialog(t *testing.T) {
	t.Parallel()
	com := hangingCommon(t, newHangingDialogWorkspace(t))
	s, _ := NewSessions(com, "")
	r, _ := NewRewind(com, "s1")

	o := NewOverlay()
	o.OpenDialog(s)
	o.OpenDialog(r)
	o.Update(sessionsLoadedMsg{owner: s, sessions: []session.Session{{ID: "s1"}}})
	require.False(t, s.loading)
	require.Len(t, s.sessions, 1)
	require.True(t, r.loading, "the front dialog is untouched")
}
