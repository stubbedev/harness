package agent

import (
	"context"
	"slices"
	"sync"
)

type subagentRegistry struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	runs    map[string]*backgroundRun
	byChild map[string]string
	inbox   *liveInbox
}

func newSubagentRegistry() *subagentRegistry {
	return &subagentRegistry{
		cancels: make(map[string]context.CancelFunc),
		runs:    make(map[string]*backgroundRun),
		byChild: make(map[string]string),
		inbox:   newLiveInbox(),
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

func (r *subagentRegistry) cancelDescendants(session string) {
	r.mu.Lock()
	tree := map[string]bool{session: true}
	var cancels []context.CancelFunc
	for grew := true; grew; {
		grew = false
		for _, run := range r.runs {
			if !tree[run.parentSession] || tree[run.childSession] {
				continue
			}
			tree[run.childSession] = true
			grew = true
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
		delete(r.runs, handle)
	}
	r.mu.Unlock()
	r.cancel(childSession)
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
	var handles []string
	for handle, run := range r.runs {
		if run.parentSession == parentSession {
			handles = append(handles, handle)
		}
	}
	slices.Sort(handles)
	return handles
}
