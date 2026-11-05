package agent

import (
	"context"
	"slices"
	"sync"
)

// subagentRegistry tracks the sub-agents the coordinator runs: the cancel
// of every live run, and the record of every background dispatch, kept
// per dispatching session for as long as that session exists.
type subagentRegistry struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	runs    map[string]*backgroundRun
	byChild map[string]string
	// byParent indexes the background runs by the session that
	// dispatched them, so a session's lookups and the walk down its tree
	// touch that session's runs rather than every run ever made.
	byParent map[string][]*backgroundRun
	inbox    *liveInbox
}

func newSubagentRegistry() *subagentRegistry {
	return &subagentRegistry{
		cancels:  make(map[string]context.CancelFunc),
		runs:     make(map[string]*backgroundRun),
		byChild:  make(map[string]string),
		byParent: make(map[string][]*backgroundRun),
		inbox:    newLiveInbox(),
	}
}

func (r *subagentRegistry) register(childSession string, cancel context.CancelFunc) (unregister func()) {
	r.mu.Lock()
	r.cancels[childSession] = cancel
	r.mu.Unlock()
	r.inbox.openSteering(childSession)
	return r.unregisterFunc(childSession)
}

func (r *subagentRegistry) registerBackground(parentSession, childSession, agentName string, cancel context.CancelFunc) (run *backgroundRun, unregister func()) {
	r.mu.Lock()
	run = &backgroundRun{
		handle:        r.newHandleLocked(),
		childSession:  childSession,
		parentSession: parentSession,
		agentName:     agentName,
		queued:        true,
	}
	r.runs[run.handle] = run
	r.byChild[childSession] = run.handle
	r.byParent[parentSession] = append(r.byParent[parentSession], run)
	r.cancels[childSession] = cancel
	r.mu.Unlock()
	r.inbox.openSteering(childSession)
	return run, r.unregisterFunc(childSession)
}

func (r *subagentRegistry) unregisterFunc(childSession string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.cancels, childSession)
			delete(r.byChild, childSession)
			r.mu.Unlock()
			r.inbox.closeSteering(childSession)
		})
	}
}

func (r *subagentRegistry) newHandleLocked() string {
	for {
		handle := newSubagentHandle()
		if _, exists := r.runs[handle]; !exists {
			return handle
		}
	}
}

func (r *subagentRegistry) cancel(childSession string) bool {
	r.mu.Lock()
	cancel, ok := r.cancels[childSession]
	r.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

func (r *subagentRegistry) cancelAll() {
	r.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(r.cancels))
	for _, cancel := range r.cancels {
		cancels = append(cancels, cancel)
	}
	r.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// cancelDescendants cancels every live background run below session,
// walking down the dispatch tree one session at a time.
func (r *subagentRegistry) cancelDescendants(session string) {
	r.mu.Lock()
	var cancels []context.CancelFunc
	seen := map[string]bool{session: true}
	for pending := []string{session}; len(pending) > 0; {
		parent := pending[0]
		pending = pending[1:]
		for _, run := range r.byParent[parent] {
			if seen[run.childSession] {
				continue
			}
			seen[run.childSession] = true
			pending = append(pending, run.childSession)
			if cancel, ok := r.cancels[run.childSession]; ok {
				cancels = append(cancels, cancel)
			}
		}
	}
	r.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (r *subagentRegistry) isRunning(childSession string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.cancels[childSession]
	return ok
}

func (r *subagentRegistry) discard(childSession string) {
	r.mu.Lock()
	if handle, ok := r.byChild[childSession]; ok {
		if run := r.runs[handle]; run != nil {
			r.byParent[run.parentSession] = slices.DeleteFunc(r.byParent[run.parentSession], func(other *backgroundRun) bool {
				return other == run
			})
			if len(r.byParent[run.parentSession]) == 0 {
				delete(r.byParent, run.parentSession)
			}
		}
		delete(r.runs, handle)
	}
	r.mu.Unlock()
	r.cancel(childSession)
}

// forgetSession drops the records of the background runs a deleted
// session dispatched, and its inbox. A run still live is cancelled: its
// dispatcher is gone and nothing can collect what it produces.
func (r *subagentRegistry) forgetSession(session string) {
	r.mu.Lock()
	runs := r.byParent[session]
	delete(r.byParent, session)
	cancels := make([]context.CancelFunc, 0, len(runs))
	for _, run := range runs {
		delete(r.runs, run.handle)
		if cancel, ok := r.cancels[run.childSession]; ok {
			cancels = append(cancels, cancel)
		}
	}
	r.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	r.inbox.forget(session)
}

func (r *subagentRegistry) lookupByChild(childSession string) (*backgroundRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	handle, ok := r.byChild[childSession]
	if !ok {
		return nil, false
	}
	run, ok := r.runs[handle]
	return run, ok
}

func (r *subagentRegistry) lookupByHandle(parentSession, handle string) (*backgroundRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[handle]
	if !ok || run.parentSession != parentSession {
		return nil, false
	}
	return run, true
}

func (r *subagentRegistry) handles(parentSession string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	runs := r.byParent[parentSession]
	handles := make([]string, 0, len(runs))
	for _, run := range runs {
		handles = append(handles, run.handle)
	}
	slices.Sort(handles)
	return handles
}
