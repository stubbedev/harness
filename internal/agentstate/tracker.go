package agentstate

import "sync"

// State is the agent state a multiplexer integration displays.
type State string

const (
	StateIdle    State = "idle"
	StateWorking State = "working"
	StateError   State = "error"
)

// Reporter publishes what a [Tracker] decides. The tracker calls it with
// its own lock held and only on a change, so implementations must not
// call back into the tracker.
type Reporter interface {
	// ReportState publishes a state transition. sessionID is the session
	// the agent is running at the time, possibly empty.
	ReportState(state State, sessionID string)
	// ReportSession publishes a change of session.
	ReportSession(sessionID string)
}

// Tracker turns the agent-lifecycle [Event] stream into deduplicated
// state and session reports. It is the one state machine every
// multiplexer integration shares: a run goes working on its first
// output or on summarization, and idle (or error, when it failed) on
// completion. Safe for concurrent use.
type Tracker struct {
	mu        sync.Mutex
	reporter  Reporter
	sessionID string
	state     State
	runActive bool
}

// NewTracker returns a tracker that starts idle and reports to r.
func NewTracker(r Reporter) *Tracker {
	return &Tracker{reporter: r, state: StateIdle}
}

// Handle applies one lifecycle event.
func (t *Tracker) Handle(ev Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch e := ev.(type) {
	case AssistantMessage:
		t.setSessionLocked(e.SessionID)
		if !t.runActive {
			t.runActive = true
			t.reportLocked(StateWorking)
		}
	case RunComplete:
		t.runActive = false
		t.setSessionLocked(e.SessionID)
		if e.Error != "" {
			t.reportLocked(StateError)
			return
		}
		t.reportLocked(StateIdle)
	case Summarizing:
		t.runActive = true
		t.reportLocked(StateWorking)
	}
}

// SetSessionID records which session the agent is running, reporting it
// when it changed. An empty id is ignored.
func (t *Tracker) SetSessionID(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.setSessionLocked(id)
}

// State returns the last reported state.
func (t *Tracker) State() State {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// SessionID returns the current session, possibly empty.
func (t *Tracker) SessionID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessionID
}

func (t *Tracker) setSessionLocked(id string) {
	if id == "" || id == t.sessionID {
		return
	}
	t.sessionID = id
	t.reporter.ReportSession(id)
}

func (t *Tracker) reportLocked(state State) {
	if state == t.state {
		return
	}
	t.state = state
	t.reporter.ReportState(state, t.sessionID)
}
