package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"charm.land/fantasy"
)

// maxLiveInboxMessages caps how many unread background-agent messages may
// pend on one dispatching session. Beyond the cap send_message fails: a
// dispatcher that never reads would otherwise let a child grow its context
// without bound. The failure is the backpressure — the child's model sees
// the error and can stop sending.
const maxLiveInboxMessages = 50

// SubagentInboxMessage is a message a running background sub-agent sent to
// the session that dispatched it.
type SubagentInboxMessage struct {
	AgentName string
	Handle    string
	// ChildSessionID identifies the sender's session so the frontend can
	// attribute the report-back to its background task.
	ChildSessionID string
	Text           string
}

// SubagentInboxSource supplies the messages background sub-agents sent to a
// session. SessionAgent drains it in PrepareStep so a message from a running
// child lands in its dispatcher's next step rather than in the child's
// dispatch result. The coordinator is the only implementation.
type SubagentInboxSource interface {
	DrainSubagentInbox(sessionID string) []SubagentInboxMessage
}

// QueueArrivalNotifier is told when a prompt is queued for a busy
// session. The coordinator is the only implementation: it stamps the
// arrival so a tool call can report it, and wakes a wait parked on the
// session so the prompt is surfaced instead of sleeping to the timeout.
type QueueArrivalNotifier interface {
	NotifyQueueArrival(sessionID string)
}

// liveInbox is the per-dispatching-session inbox for messages from running
// background sub-agents. Unlike the per-child completion inbox, entries are
// keyed by the parent session and many children may write the same key
// concurrently, so appends and drains share one mutex and preserve arrival
// order across children.
//
// Wakeups use close-and-replace channels: notify closes the current channel
// (waking every waiter, however many are blocked on it) and installs a fresh
// one. A waiter re-fetches the channel each loop iteration before it drains,
// which makes record -> notify strictly ordered against drain -> select, so
// no wakeup can be lost between the state check and the wait.
type liveInbox struct {
	mu      sync.Mutex
	items   map[string][]SubagentInboxMessage
	signals signalMap
}

func newLiveInbox() *liveInbox {
	return &liveInbox{
		items: make(map[string][]SubagentInboxMessage),
	}
}

// signalChan returns the current wakeup channel for a session. Fetch it
// before draining state; see the type comment for the ordering argument.
func (in *liveInbox) signalChan(parentSessionID string) chan struct{} {
	return in.signals.Chan(parentSessionID)
}

// notify wakes everyone waiting on the session's inbox.
func (in *liveInbox) notify(parentSessionID string) {
	in.signals.Broadcast(parentSessionID)
}

// record appends a message to a session's inbox, refusing once the cap is
// reached so a sender that outpaces its dispatcher gets an error instead of
// growing the dispatcher's context without bound.
func (in *liveInbox) record(parentSessionID string, msg SubagentInboxMessage) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	pending := in.items[parentSessionID]
	if len(pending) >= maxLiveInboxMessages {
		return fmt.Errorf(
			"the orchestrator already has %d unread messages pending and is not reading them; stop sending messages and finish your run",
			len(pending),
		)
	}
	in.items[parentSessionID] = append(pending, msg)
	return nil
}

// drain takes every pending message for a session.
func (in *liveInbox) drain(parentSessionID string) []SubagentInboxMessage {
	in.mu.Lock()
	defer in.mu.Unlock()
	msgs := in.items[parentSessionID]
	delete(in.items, parentSessionID)
	return msgs
}

// drainFrom takes the pending messages sent via the given handles, leaving
// the rest for later readers.
func (in *liveInbox) drainFrom(parentSessionID string, handles map[string]bool) []SubagentInboxMessage {
	in.mu.Lock()
	defer in.mu.Unlock()
	items := in.items[parentSessionID]
	var taken, kept []SubagentInboxMessage
	for _, item := range items {
		if handles[item.Handle] {
			taken = append(taken, item)
		} else {
			kept = append(kept, item)
		}
	}
	if len(kept) == 0 {
		delete(in.items, parentSessionID)
	} else {
		in.items[parentSessionID] = kept
	}
	return taken
}

// backgroundRun tracks one sub-agent dispatched in the background: the agent
// tool returned a handle immediately, and the child runs on its own goroutine
// until it completes, fails, or is cancelled. Its final response (including
// any SubagentStop hook context) is kept here until the dispatcher collects
// it with the wait tool; records are never evicted, so a result survives the
// turn that started it and remains collectable in later turns.
type backgroundRun struct {
	handle        string
	childSession  string
	parentSession string
	agentName     string

	mu        sync.Mutex
	finished  bool
	status    string
	result    string
	resultErr bool
	worktree  *WorktreeResult
}

// finish records the terminal state exactly once. Later calls are no-ops so
// a duplicate finish cannot overwrite the collected result.
func (r *backgroundRun) finish(status string, resp fantasy.ToolResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return
	}
	r.finished = true
	r.status = status
	r.result = resp.Content
	r.resultErr = resp.IsError
	var metadata struct {
		Worktree *WorktreeResult `json:"worktree"`
	}
	if json.Unmarshal([]byte(resp.Metadata), &metadata) == nil {
		r.worktree = metadata.Worktree
	}
}

// snapshot returns the run's current state.
func (r *backgroundRun) snapshot() (finished bool, status, result string, resultErr bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finished, r.status, r.result, r.resultErr
}

// isFinished reports whether the run has terminated.
func (r *backgroundRun) isFinished() bool {
	finished, _, _, _ := r.snapshot()
	return finished
}

// newSubagentHandle mints a short, model-friendly handle for a background
// dispatch ("bg-" plus 8 hex chars). Collisions are handled by the caller.
func newSubagentHandle() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is a broken system; fall back to something
		// still unique-ish rather than panicking in the dispatch path.
		return fmt.Sprintf("bg-%x", time.Now().UnixNano())
	}
	return "bg-" + hex.EncodeToString(b[:])
}
