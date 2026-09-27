// Package herdr provides native integration with the herdr terminal
// multiplexer. When Harness runs inside a herdr-managed pane it reports
// agent state (idle, working, blocked) and session identity over
// herdr's Unix socket API so herdr can display accurate status without
// screen scraping.
//
// The client consumes the neutral agent-lifecycle vocabulary from
// internal/agentstate rather than raw proto or domain types: callers
// translate with agentstate.Translate before forwarding, and the shared
// agentstate.Tracker decides the transitions. This keeps the client
// decoupled from both the proto and internal domain layers.
package herdr

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/stubbedev/harness/internal/agentstate"
	"github.com/stubbedev/harness/internal/crash"
)

// State values matching herdr's PaneAgentState enum.
const (
	stateIdle    = "idle"
	stateWorking = "working"
	stateBlocked = "blocked"
)

// sender abstracts the transport layer for reporting state to herdr.
// Production uses a Unix socket; tests use a recorder.
type sender interface {
	send(req reportRequest) error
	close()
}

// Client reports Harness agent state to a running herdr instance.
type Client struct {
	socketPath string
	paneID     string
	tracker    *agentstate.Tracker

	// mu guards the request fields below: every request carries the
	// current session and a strictly increasing seq.
	mu        sync.Mutex
	sessionID string
	state     string
	seq       uint64

	snd sender
}

// defaultClient is the process-wide herdr client. Initialized once
// via Init(). All integration sites share this single instance so
// only one Unix socket connection exists per process.
var (
	defaultClient *Client
	initOnce      sync.Once
)

// Init returns the process-wide herdr Client, creating it on first
// call from environment variables. Returns nil when Harness is not
// running inside a herdr pane. Safe to call from any goroutine.
func Init() *Client {
	initOnce.Do(func() {
		defaultClient = newFromEnv()
	})
	return defaultClient
}

func newFromEnv() *Client {
	if os.Getenv("HERDR_ENV") != "1" {
		return nil
	}
	// A test binary inherits the launching shell's HERDR_* env, so
	// without this it would attach to the developer's live pane and
	// release its agent on teardown. Skip herdr entirely under test.
	if flag.Lookup("test.v") != nil {
		slog.Debug("Herdr integration disabled: running under go test")
		return nil
	}
	socketPath := os.Getenv("HERDR_SOCKET_PATH")
	paneID := os.Getenv("HERDR_PANE_ID")
	if socketPath == "" || paneID == "" {
		slog.Debug(
			"Herdr integration disabled: incomplete environment",
			"has_socket", socketPath != "",
			"has_pane_id", paneID != "",
		)
		return nil
	}
	c := newClient(socketPath, paneID, uint64(time.Now().UnixNano()), newUnixSender(socketPath))
	c.registerInitial()
	return c
}

// newClient returns a Client reporting on paneID over snd, numbering
// requests from seq.
func newClient(socketPath, paneID string, seq uint64, snd sender) *Client {
	c := &Client{
		socketPath: socketPath,
		paneID:     paneID,
		state:      stateIdle,
		seq:        seq,
		snd:        snd,
	}
	c.tracker = agentstate.NewTracker(c)
	return c
}

// registerInitial sends an initial idle-state report to herdr so the
// pane knows about the agent immediately, not just after the first
// event. Called once during client creation. Bypasses the dedup
// check since the initial state must always be reported regardless
// of redundancy.
//
// herdr remembers the highest seq it has seen per source for the
// lifetime of a pane and silently drops any report with a seq that
// is not strictly greater. Because harness seeds seq from the wall
// clock at startup (see newFromEnv), a restarted harness in the same
// pane always reports above the previous run's high-water mark, so
// the first report is accepted instead of being rejected as stale.
func (c *Client) registerInitial() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snd.send(c.newRequestLocked("pane.report_agent", "init", stateIdle))
}

// Close releases the agent's authority on the pane and shuts down
// the background writer. Safe to call on a nil client.
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.releaseAgent()
	c.snd.close()
}

// releaseAgent sends a pane.release_agent request to herdr so the
// pane is freed for a new agent to claim authority. This is the
// clean-shutdown protocol per herdr's socket API. Sends directly
// on the socket to ensure delivery even if the write loop is busy.
func (c *Client) releaseAgent() {
	c.mu.Lock()
	defer c.mu.Unlock()
	req := c.newRequestLocked("pane.release_agent", "release", "")
	if err := dialSend(c.socketPath, req); err != nil {
		slog.Debug("Herdr release_agent failed", "error", err)
	}
}

// HandleEvent processes a single agent-lifecycle event and reports
// state changes. Safe to call from any goroutine.
func (c *Client) HandleEvent(ev agentstate.Event) {
	if c == nil {
		return
	}
	c.tracker.Handle(ev)
}

// SetSessionID sets the session ID for reporting. Call this when the
// session is created or resolved, before events start flowing.
func (c *Client) SetSessionID(id string) {
	if c == nil {
		return
	}
	c.tracker.SetSessionID(id)
}

// ReportState implements [agentstate.Reporter] as a pane.report_agent
// request. herdr's pane states have no error, so a failed run reports
// idle: the agent is waiting for input again either way.
func (c *Client) ReportState(state agentstate.State, sessionID string) {
	herdrState := stateWorking
	if state != agentstate.StateWorking {
		herdrState = stateIdle
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionID = sessionID
	if herdrState == c.state {
		return
	}
	c.state = herdrState
	c.snd.send(c.newRequestLocked("pane.report_agent", "report", herdrState))
}

// ReportSession implements [agentstate.Reporter]. herdr carries the
// session on every request rather than as a report of its own, so it is
// only recorded.
func (c *Client) ReportSession(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionID = sessionID
}

// newRequestLocked builds a seq-stamped JSON-RPC request to herdr.
// Must be called with c.mu held. Every request increments c.seq so
// herdr accepts it as strictly newer than the last (see
// registerInitial for why monotonic seq matters). State is empty for
// requests that carry no agent state, such as pane.release_agent.
func (c *Client) newRequestLocked(method, idPrefix, state string) reportRequest {
	c.seq++
	return reportRequest{
		ID:     fmt.Sprintf("harness:%s:%d", idPrefix, time.Now().UnixNano()),
		Method: method,
		Params: reportParams{
			PaneID:         c.paneID,
			Source:         "harness",
			Agent:          "harness",
			State:          state,
			Seq:            c.seq,
			AgentSessionID: c.sessionID,
		},
	}
}

// reportRequest is the JSON-RPC envelope sent to herdr.
type reportRequest struct {
	ID     string       `json:"id"`
	Method string       `json:"method"`
	Params reportParams `json:"params"`
}

// reportParams carries the agent state payload.
type reportParams struct {
	PaneID         string `json:"pane_id"`
	Source         string `json:"source"`
	Agent          string `json:"agent"`
	State          string `json:"state"`
	Seq            uint64 `json:"seq"`
	AgentSessionID string `json:"agent_session_id"`
}

// requestTimeout bounds a single dial or write to herdr.
const requestTimeout = 500 * time.Millisecond

// unixSender sends JSON-RPC requests over a Unix domain socket using
// a single background writer goroutine and a buffered channel. This
// serializes writes and avoids spawning unbounded goroutines under
// high event throughput. The writer keeps one long-lived connection
// open across reports and re-dials after a failure; the report that
// hit the failure falls back to a dial-once send, and the long-lived
// connection is only retried by the next report, so a missing server
// never turns into a busy retry loop.
type unixSender struct {
	socketPath string
	ch         chan reportRequest
	cancel     context.CancelFunc
}

func newUnixSender(socketPath string) *unixSender {
	ctx, cancel := context.WithCancel(context.Background())
	s := &unixSender{
		socketPath: socketPath,
		ch:         make(chan reportRequest, 16),
		cancel:     cancel,
	}
	crash.Go("herdr.writeLoop", func() { s.writeLoop(ctx) })
	return s
}

func (s *unixSender) send(req reportRequest) error {
	select {
	case s.ch <- req:
	default:
		// Drop if the buffer is full. State reports are
		// best-effort; blocking the agent is worse than
		// missing a transition.
	}
	return nil
}

func (s *unixSender) close() {
	s.cancel()
}

// writeLoop owns the long-lived connection to herdr. It is the only
// goroutine that touches the connection, so no extra locking is
// needed beyond the channel handoff in send.
func (s *unixSender) writeLoop(ctx context.Context) {
	var conn net.Conn
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()
	for {
		select {
		case req, ok := <-s.ch:
			if !ok {
				return
			}
			conn = s.deliver(conn, req)
		case <-ctx.Done():
			return
		}
	}
}

// deliver sends req over conn, dialing first when conn is nil, and
// returns the connection to use for the next report. A failed write
// closes the connection and retries req once over a fresh dial-once
// connection; a failed dial counts as that report's single attempt.
// Either way the next report re-attempts the long-lived connection.
func (s *unixSender) deliver(conn net.Conn, req reportRequest) net.Conn {
	if conn == nil {
		c, err := dial(s.socketPath)
		if err != nil {
			slog.Debug("Herdr report failed", "error", err)
			return nil
		}
		conn = c
		go discardResponses(conn)
	}
	if err := writeReport(conn, req); err != nil {
		_ = conn.Close()
		slog.Debug("Herdr report failed", "error", err)
		if err := dialSend(s.socketPath, req); err != nil {
			slog.Debug("Herdr report failed", "error", err)
		}
		return nil
	}
	return conn
}

// discardResponses drains herdr's responses on conn until the
// connection closes, so replies to reused connections never pile up
// in the receive buffer. The writer closing conn unblocks it.
func discardResponses(conn net.Conn) {
	_, _ = io.Copy(io.Discard, conn)
}

// dial opens a Unix socket connection to herdr.
func dial(socketPath string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	dialer := net.Dialer{}
	return dialer.DialContext(ctx, "unix", socketPath)
}

// writeReport marshals req and writes it to conn as a single line.
func writeReport(conn net.Conn, req reportRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	_ = conn.SetWriteDeadline(time.Now().Add(requestTimeout))
	_, err = conn.Write(data)
	return err
}

// dialSend opens a short-lived Unix socket connection to herdr,
// sends a single JSON-RPC request, and drains the response. It is
// the fallback path when the long-lived connection fails.
func dialSend(socketPath string, req reportRequest) error {
	conn, err := dial(socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(requestTimeout))

	if err := writeReport(conn, req); err != nil {
		return err
	}

	// Drain the response to complete the request cycle.
	_, _ = io.Copy(io.Discard, conn)
	return nil
}
