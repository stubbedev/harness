package tmux

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/agentstate"
)

// TestControlClientIntegration drives a real tmux server on a private
// socket: it spawns the control-mode client, reports state transitions
// through it, and verifies the pane user options the status line would
// read. Skipped when tmux is not installed.
func TestControlClientIntegration(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}

	socket := filepath.Join(t.TempDir(), "server")
	tmux := func(args ...string) (string, error) {
		cmd := exec.CommandContext(t.Context(), "tmux", append([]string{"-S", socket}, args...)...)
		out, err := cmd.Output()
		return string(out), err
	}

	if _, err := tmux("-f", "/dev/null", "new-session", "-d", "-s", "itest"); err != nil {
		t.Skipf("failed to start scratch tmux server: %v", err)
	}
	t.Cleanup(func() {
		_, _ = tmux("kill-server")
	})

	paneID, err := tmux("display-message", "-p", "-t", "itest", "#{pane_id}")
	require.NoError(t, err)
	sessionID, err := tmux("display-message", "-p", "-t", "itest", "#{session_id}")
	require.NoError(t, err)
	pane := strings.TrimSpace(paneID)
	session := strings.TrimSpace(sessionID)

	snd, err := newControlSender(socket, session)
	require.NoError(t, err)
	c := newClient(pane, snd)
	c.registerInitial()

	show := func(option string) (string, error) {
		out, err := tmux("show-options", "-p", "-v", "-t", pane, option)
		return strings.TrimSpace(out), err
	}
	waitFor := func(option, want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			got, _ := show(option)
			if got == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("pane option %s: got %q, want %q", option, got, want)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	waitFor(stateOption, "idle")

	c.SetSessionID("sess-1")
	waitFor(sessionOption, "sess-1")

	c.HandleEvent(agentstate.AssistantMessage{SessionID: "sess-1"})
	waitFor(stateOption, "working")

	c.HandleEvent(agentstate.RunComplete{SessionID: "sess-1", Error: "boom"})
	waitFor(stateOption, "error")

	c.HandleEvent(agentstate.AssistantMessage{SessionID: "sess-1"})
	waitFor(stateOption, "working")

	c.HandleEvent(agentstate.RunComplete{SessionID: "sess-1"})
	waitFor(stateOption, "idle")

	c.Close()
	waitUnset := func(option string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, err := show(option)
			if err != nil {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("pane option %s still set after Close", option)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	waitUnset(stateOption)
	waitUnset(sessionOption)
}
