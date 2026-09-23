package agent

import "sync"

// signalMap is a per-key close-and-replace wakeup channel. A waiter
// fetches the key's channel before reading the state it waits on and
// selects on it after; broadcast closes and replaces the channel under
// the same lock, so a change landing between the read and the select
// still wakes the waiter.
type signalMap struct {
	mu    sync.Mutex
	chans map[string]chan struct{}
}

// Chan returns the key's current wakeup channel.
func (s *signalMap) Chan(key string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chanLocked(key)
}

// Broadcast wakes everyone waiting on the key.
func (s *signalMap) Broadcast(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broadcastLocked(key)
}

func (s *signalMap) chanLocked(key string) chan struct{} {
	if s.chans == nil {
		s.chans = map[string]chan struct{}{}
	}
	ch := s.chans[key]
	if ch == nil {
		ch = make(chan struct{})
		s.chans[key] = ch
	}
	return ch
}

func (s *signalMap) broadcastLocked(key string) {
	if ch := s.chans[key]; ch != nil {
		close(ch)
	}
	if s.chans == nil {
		s.chans = map[string]chan struct{}{}
	}
	s.chans[key] = make(chan struct{})
}

// queueArrivalSignals carries two facts about prompts queued for a busy
// session, both keyed by session:
//
//   - a monotonic epoch, bumped once per queued prompt, so a tool call
//     can tell "a prompt arrived while I ran" from a stable queue by
//     comparing snapshots taken around its execution;
//   - a wakeup channel, so a wait blocked on the session returns the
//     moment a prompt lands instead of sleeping to its timeout.
//
// Both change under the signal map's lock, so a waiter that reads the
// epoch after fetching the channel never misses an arrival.
type queueArrivalSignals struct {
	signalMap
	epochs map[string]uint64
}

func newQueueArrivalSignals() *queueArrivalSignals {
	return &queueArrivalSignals{epochs: map[string]uint64{}}
}

// notify records one queued prompt for the session and wakes every
// waiter parked on it.
func (q *queueArrivalSignals) notify(sessionID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.epochs[sessionID]++
	q.broadcastLocked(sessionID)
}

// chanFor returns the session's current wakeup channel. Fetch it before
// reading the epoch and select on it after.
func (q *queueArrivalSignals) chanFor(sessionID string) chan struct{} {
	return q.Chan(sessionID)
}

// epoch returns how many prompts have been queued for the session since
// it was first seen.
func (q *queueArrivalSignals) epoch(sessionID string) uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.epochs[sessionID]
}
