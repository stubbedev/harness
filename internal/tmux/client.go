// Package tmux provides native integration with the tmux terminal
// multiplexer. When Harness runs inside a tmux pane it reports agent
// state and session identity as pane user options (@harness-state,
// @harness-session) through tmux's control-mode IPC, so a status line
// can display accurate per-pane agent status without hooks or polling.
//
// tmux has no direct socket API for external programs: the server
// socket speaks an undocumented, version-coupled protocol. Control
// mode (tmux -C attach-session) is the sanctioned programmatic
// interface — a long-lived client that speaks plain tmux commands over
// its stdin and emits %-prefixed notifications on stdout. One such
// client is spawned per Harness process; every report is a single
// command written to it, so no subprocess is spawned per event.
package tmux

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/stubbedev/harness/internal/agentstate"
	"github.com/stubbedev/harness/internal/crash"
)

// Pane options written by this package.
const (
	stateOption   = "@harness-state"
	sessionOption = "@harness-session"
)

// sender abstracts the command transport. Production uses a tmux
// control-mode client; tests use a recorder.
type sender interface {
	send(line string) error
	close()
}

// Client reports Harness agent state to the tmux server managing the
// pane Harness is running in. Nil-safe: every method can be called on
// a nil Client, which is what Init returns outside tmux.
type Client struct {
	paneID  string
	tracker *agentstate.Tracker

	// mu serializes commands to snd.
	mu  sync.Mutex
	snd sender
}

// defaultClient is the process-wide tmux client. Initialized once via
// Init(). All integration sites share this single instance so only one
// control-mode client exists per process.
var (
	defaultClient *Client
	initOnce      sync.Once
)

// Init returns the process-wide tmux Client, creating it on first call
// from the TMUX and TMUX_PANE environment variables. Returns nil when
// Harness is not running inside a tmux pane. Safe to call from any
// goroutine.
func Init() *Client {
	initOnce.Do(func() {
		// A test binary inherits the launching shell's TMUX env, so
		// without this it would attach a control client to the
		// developer's live server and write pane options to their
		// active pane. Integration tests construct clients directly
		// instead. Skip tmux entirely under go test.
		if flag.Lookup("test.v") != nil {
			slog.Debug("Tmux integration disabled: running under go test")
			return
		}
		defaultClient = newFromEnv()
	})
	return defaultClient
}

// newFromEnv builds a Client from the pane environment, returning nil
// when the variables are missing or malformed.
func newFromEnv() *Client {
	socket, sessionID, paneID, ok := parseEnv(os.Getenv("TMUX"), os.Getenv("TMUX_PANE"))
	if !ok {
		return nil
	}
	snd, err := newControlSender(socket, sessionID)
	if err != nil {
		slog.Debug("Tmux integration disabled: control client failed to start", "error", err)
		return nil
	}
	c := newClient(paneID, snd)
	c.registerInitial()
	return c
}

// parseEnv extracts the tmux server socket, the session ID, and the
// pane ID from the environment tmux sets for processes in a pane.
// TMUX has the form "<socket>,<server-pid>,<session-id-number>".
func parseEnv(tmux, tmuxPane string) (socket, sessionID, paneID string, ok bool) {
	parts := strings.Split(tmux, ",")
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return "", "", "", false
	}
	if tmuxPane == "" {
		return "", "", "", false
	}
	return parts[0], "$" + parts[2], tmuxPane, true
}

// newClient returns a Client reporting on paneID over snd.
func newClient(paneID string, snd sender) *Client {
	c := &Client{paneID: paneID, snd: snd}
	c.tracker = agentstate.NewTracker(c)
	return c
}

// registerInitial reports idle state immediately so the pane knows
// about the agent from startup, not just after the first event.
func (c *Client) registerInitial() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sendfLocked(stateOption, string(c.tracker.State()))
}

// Close unsets the pane options this client wrote and shuts down the
// control client. Safe to call on a nil client.
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sendUnsetLocked(stateOption)
	c.sendUnsetLocked(sessionOption)
	c.snd.close()
}

// SetSessionID records which Harness session the pane is running and
// publishes it as the @harness-session pane option.
func (c *Client) SetSessionID(id string) {
	if c == nil {
		return
	}
	c.tracker.SetSessionID(id)
}

// HandleEvent processes a single agent-lifecycle event and reports
// state changes. Safe to call from any goroutine.
func (c *Client) HandleEvent(ev agentstate.Event) {
	if c == nil {
		return
	}
	c.tracker.Handle(ev)
}

// ReportState implements [agentstate.Reporter] as the @harness-state
// pane option.
func (c *Client) ReportState(state agentstate.State, _ string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sendfLocked(stateOption, string(state))
}

// ReportSession implements [agentstate.Reporter] as the
// @harness-session pane option.
func (c *Client) ReportSession(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sendfLocked(sessionOption, sessionID)
}

// sendfLocked writes "set-option -p -t <pane> <option> <value>" as a
// control-mode command. Values are restricted to single tokens: the
// line-oriented protocol means a value with whitespace or newlines
// would corrupt the stream, so anything unsafe is dropped. Must be
// called with c.mu held.
func (c *Client) sendfLocked(option, value string) {
	if strings.ContainsAny(value, " \t\n\r\"'") {
		slog.Debug("Tmux report dropped: unsafe value", "option", option)
		return
	}
	if err := c.snd.send("set-option -p -t " + c.paneID + " " + option + " " + value); err != nil {
		slog.Debug("Tmux report failed", "error", err)
	}
}

// sendUnsetLocked writes "set-option -p -t <pane> -u <option>" so the
// option stops appearing in the pane's environment. Must be called
// with c.mu held.
func (c *Client) sendUnsetLocked(option string) {
	if err := c.snd.send("set-option -p -t " + c.paneID + " -u " + option); err != nil {
		slog.Debug("Tmux unset failed", "error", err)
	}
}

// controlSender owns the tmux control-mode client process: commands
// are written to its stdin and its stdout is drained asynchronously.
// Once the process exits, send returns an error and further reports
// are dropped.
type controlSender struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser

	mu   sync.Mutex
	dead bool
}

// newControlSender spawns "tmux -C -S <socket> attach-session -t
// <sessionID>" with TMUX stripped from its environment (the nesting
// guard refuses clients that appear to run inside tmux) and silences
// the %output notification firehose.
func newControlSender(socket, sessionID string) (*controlSender, error) {
	cmd := exec.CommandContext(context.Background(), "tmux", "-C", "-S", socket, "attach-session", "-t", sessionID)
	cmd.Env = withoutTMUX(os.Environ())
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 5 * time.Second

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	s := &controlSender{cmd: cmd, stdin: stdin}
	crash.Go("tmux.readLoop", func() { s.drain(stdout) })

	// The control client must not receive pane content: it is a
	// reporter, not a renderer.
	if err := s.send("refresh-client -f no-output"); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

// drain consumes the control client's stdout until it exits. Command
// results arrive as %begin/%end (or %error) blocks and state changes
// as %-prefixed notifications; only errors are worth logging.
func (s *controlSender) drain(stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if line := sc.Text(); strings.HasPrefix(line, "%error") {
			slog.Debug("Tmux control client error", "line", line)
		}
	}
}

func (s *controlSender) send(line string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead {
		return errDeadSender
	}
	if _, err := io.WriteString(s.stdin, line+"\n"); err != nil {
		s.dead = true
		return err
	}
	return nil
}

func (s *controlSender) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead {
		return
	}
	s.dead = true
	_ = s.stdin.Close()
	// Wait in the background: with WaitDelay set this returns once the
	// process exits or is reaped after the grace period.
	crash.Go("tmux.controlClient.wait", func() {
		if err := s.cmd.Wait(); err != nil {
			slog.Debug("Tmux control client exited", "error", err)
		}
	})
}

// errDeadSender marks sends against an exited control client.
var errDeadSender = errors.New("tmux control client is not running")

// withoutTMUX returns env with TMUX removed so a spawned tmux client
// is not treated as nested.
func withoutTMUX(env []string) []string {
	result := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, "TMUX=") {
			continue
		}
		result = append(result, e)
	}
	return result
}
