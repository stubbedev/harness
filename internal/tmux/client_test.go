package tmux

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/agentstate"
)

// recorder is a test sender capturing every command line.
type recorder struct {
	mu     sync.Mutex
	lines  []string
	closed bool
}

func (r *recorder) send(line string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
	return nil
}

func (r *recorder) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
}

func (r *recorder) snapshot() ([]string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...), r.closed
}

func newTestClient() (*Client, *recorder) {
	rec := &recorder{}
	return newClient("%42", rec), rec
}

func TestParseEnv(t *testing.T) {
	tests := []struct {
		name    string
		tmux    string
		pane    string
		socket  string
		session string
		paneID  string
		ok      bool
	}{
		{
			name:    "valid",
			tmux:    "/run/user/1000/tmux-1000/default,1234,5",
			pane:    "%17",
			socket:  "/run/user/1000/tmux-1000/default",
			session: "$5",
			paneID:  "%17",
			ok:      true,
		},
		{name: "missing pane", tmux: "/tmp/sock,1,0", pane: "", ok: false},
		{name: "two fields", tmux: "/tmp/sock,1", pane: "%1", ok: false},
		{name: "empty socket", tmux: ",1,0", pane: "%1", ok: false},
		{name: "empty session", tmux: "/tmp/sock,1,", pane: "%1", ok: false},
		{name: "empty env", tmux: "", pane: "%1", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			socket, session, paneID, ok := parseEnv(tt.tmux, tt.pane)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.socket, socket)
			assert.Equal(t, tt.session, session)
			assert.Equal(t, tt.paneID, paneID)
		})
	}
}

func TestInitialReportIsIdle(t *testing.T) {
	c, rec := newTestClient()
	c.registerInitial()
	lines, _ := rec.snapshot()
	require.Equal(t, []string{"set-option -p -t %42 @harness-state idle"}, lines)
}

func TestLifecycleTransitions(t *testing.T) {
	c, rec := newTestClient()
	c.registerInitial()

	c.HandleEvent(agentstate.AssistantMessage{SessionID: "s1"})
	c.HandleEvent(agentstate.AssistantMessage{SessionID: "s1"})
	c.HandleEvent(agentstate.Summarizing{})
	c.HandleEvent(agentstate.RunComplete{SessionID: "s1"})

	lines, _ := rec.snapshot()
	require.Equal(t, []string{
		"set-option -p -t %42 @harness-state idle",
		"set-option -p -t %42 @harness-session s1",
		"set-option -p -t %42 @harness-state working",
		"set-option -p -t %42 @harness-state idle",
	}, lines)
}

func TestRunCompleteErrorReportsErrorState(t *testing.T) {
	c, rec := newTestClient()
	c.HandleEvent(agentstate.AssistantMessage{SessionID: ""})
	c.HandleEvent(agentstate.RunComplete{SessionID: "s1", Error: "boom"})
	lines, _ := rec.snapshot()
	require.Equal(t, []string{
		"set-option -p -t %42 @harness-state working",
		"set-option -p -t %42 @harness-session s1",
		"set-option -p -t %42 @harness-state error",
	}, lines)
}

func TestNewRunAfterErrorReturnsToWorking(t *testing.T) {
	c, rec := newTestClient()
	c.HandleEvent(agentstate.RunComplete{SessionID: "s1", Error: "boom"})
	c.HandleEvent(agentstate.AssistantMessage{SessionID: "s1"})
	lines, _ := rec.snapshot()
	require.Equal(t, []string{
		"set-option -p -t %42 @harness-session s1",
		"set-option -p -t %42 @harness-state error",
		"set-option -p -t %42 @harness-state working",
	}, lines)
}

func TestSetSessionIDDeduped(t *testing.T) {
	c, rec := newTestClient()
	c.SetSessionID("s1")
	c.SetSessionID("s1")
	c.SetSessionID("")
	c.SetSessionID("s2")
	lines, _ := rec.snapshot()
	require.Equal(t, []string{
		"set-option -p -t %42 @harness-session s1",
		"set-option -p -t %42 @harness-session s2",
	}, lines)
}

func TestCloseUnsetsOptions(t *testing.T) {
	c, rec := newTestClient()
	c.SetSessionID("s1")
	c.Close()
	lines, closed := rec.snapshot()
	require.Equal(t, []string{
		"set-option -p -t %42 @harness-session s1",
		"set-option -p -t %42 -u @harness-state",
		"set-option -p -t %42 -u @harness-session",
	}, lines)
	assert.True(t, closed)
}

func TestNilClientIsInert(t *testing.T) {
	var c *Client
	assert.NotPanics(t, func() {
		c.Close()
		c.SetSessionID("s1")
		c.HandleEvent(agentstate.AssistantMessage{})
		c.registerInitial()
	})
}

func TestUnsafeValuesDropped(t *testing.T) {
	c, rec := newTestClient()
	c.SetSessionID("evil value")
	c.SetSessionID("newline\nvalue")
	lines, _ := rec.snapshot()
	assert.Empty(t, lines)
}

func TestWithoutTMUX(t *testing.T) {
	env := []string{"PATH=/bin", "TMUX=/sock,1,0", "TMUX_PANE=%1", "HOME=/home/u"}
	got := withoutTMUX(env)
	assert.Equal(t, []string{"PATH=/bin", "TMUX_PANE=%1", "HOME=/home/u"}, got)
}
