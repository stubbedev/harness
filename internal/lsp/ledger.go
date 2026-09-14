package lsp

import (
	"maps"
	"slices"
	"sync"
)

// Ledger remembers which diagnostics a session has already been shown so a
// report can carry only what changed. Language servers republish their whole
// view of a file on every change, so without this every edit and every read
// re-sends the same warnings, and a long session pays for the same twenty
// lines of "unused variable" dozens of times.
//
// Entries are keyed by a fingerprint that deliberately leaves out the line and
// column: an edit above an existing problem shifts every diagnostic below it,
// and re-reporting all of them as new would defeat the point. The stored value
// is the formatted line as it last read, so a diagnostic that goes away can
// still be named when it is reported as resolved.
type Ledger struct {
	mu       sync.Mutex
	sessions map[string]map[string]string
}

// NewLedger creates an empty ledger.
func NewLedger() *Ledger {
	return &Ledger{sessions: make(map[string]map[string]string)}
}

// Diff records current as everything the session has now been shown and
// reports what changed since the last call: added holds the formatted lines
// for diagnostics that are new, resolved holds the lines, as they last read,
// for diagnostics that are gone. Both are sorted so repeated reports of the
// same state read the same way.
//
// The whole read-modify-write is held under one lock: tool calls run in
// parallel, and two reports racing here would each see the other's
// diagnostics as already-reported.
func (l *Ledger) Diff(session string, current map[string]string) (added, resolved []string) {
	if l == nil {
		return nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	previous := l.sessions[session]
	for fingerprint, line := range current {
		if _, seen := previous[fingerprint]; !seen {
			added = append(added, line)
		}
	}
	for fingerprint, line := range previous {
		if _, stillThere := current[fingerprint]; !stillThere {
			resolved = append(resolved, line)
		}
	}
	l.sessions[session] = maps.Clone(current)

	slices.Sort(added)
	slices.Sort(resolved)
	return added, resolved
}

// Record marks every diagnostic in current as already shown without reporting
// anything. Use it after a full listing, so the next diff is measured against
// what the full listing already said.
func (l *Ledger) Record(session string, current map[string]string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sessions[session] = maps.Clone(current)
}

// Forget drops what a session has been shown, so the next report starts from
// nothing. Called when the transcript the diagnostics were reported into is no
// longer in the model's context — after a summarization, say — since
// suppressing them as "already seen" would then hide them for good.
func (l *Ledger) Forget(session string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.sessions, session)
}
