package agent

import (
	"sync"
	"sync/atomic"
)

// queueArrivalSignals carries two facts about prompts queued for a busy
// session, both keyed by session:
//
//   - a monotonic epoch, bumped once per queued prompt, so a tool call
//     can tell "a prompt arrived while I ran" from a stable queue by
//     comparing snapshots taken around its execution;
//   - a close-and-replace wakeup channel (the pattern liveInbox uses),
//     so a wait blocked on the session returns the moment a prompt
//     lands instead of sleeping to its timeout.
type queueArrivalSignals struct {
	mu     sync.Mutex
	chans  map[string]chan struct{}
	epochs map[string]*atomic.Uint64
}

func newQueueArrivalSignals() *queueArrivalSignals {
	return &queueArrivalSignals{
		chans:  map[string]chan struct{}{},
		epochs: map[string]*atomic.Uint64{},
	}
}

// notify records one queued prompt for the session and wakes every
// waiter parked on it.
func (q *queueArrivalSignals) notify(sessionID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	e := q.epochs[sessionID]
	if e == nil {
		e = &atomic.Uint64{}
		q.epochs[sessionID] = e
	}
	e.Add(1)
	if ch := q.chans[sessionID]; ch != nil {
		close(ch)
	}
	q.chans[sessionID] = make(chan struct{})
}

// chanFor returns the session's current wakeup channel. Fetch it before
// reading the epoch and select on it after: notify closes and replaces
// under the same lock, so an arrival between the epoch read and the
// select still wakes the waiter.
func (q *queueArrivalSignals) chanFor(sessionID string) chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	ch := q.chans[sessionID]
	if ch == nil {
		ch = make(chan struct{})
		q.chans[sessionID] = ch
	}
	return ch
}

// epoch returns how many prompts have been queued for the session since
// it was first seen.
func (q *queueArrivalSignals) epoch(sessionID string) uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	if e := q.epochs[sessionID]; e != nil {
		return e.Load()
	}
	return 0
}
