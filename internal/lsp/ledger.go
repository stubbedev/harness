package lsp

import (
	"maps"
	"slices"
	"strings"
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
	// generations records, per session, the diagnostics generation the
	// session was last diffed at. The same generation means the same
	// diagnostics, so a report at it has nothing to say.
	generations map[string]string
}

// ResolvedDiagnostic is a problem the servers no longer report. It carries the
// fingerprint it was reported under and the line as it last read, so a report
// can name what went away and a consumer holding a live snapshot can match
// the problem up with where it last sat.
type ResolvedDiagnostic struct {
	Fingerprint string
	Line        string
}

// NewLedger creates an empty ledger.
func NewLedger() *Ledger {
	return &Ledger{sessions: make(map[string]map[string]string), generations: make(map[string]string)}
}

// Diff records current as everything the session has now been shown and
// reports what changed since the last call: added holds the formatted lines
// for diagnostics that are new, resolved holds what is gone, keyed by
// fingerprint with the line as it last read. Both are sorted so repeated
// reports of the same state read the same way.
//
// The whole read-modify-write is held under one lock: tool calls run in
// parallel, and two reports racing here would each see the other's
// diagnostics as already-reported.
func (l *Ledger) Diff(session string, current map[string]string) (added []string, resolved []ResolvedDiagnostic) {
	if l == nil {
		return nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.generations, session)
	return l.diffLocked(session, current)
}

// DiffAt is Diff for diagnostics at generation, a value that changes
// whenever any server's diagnostics do (see Manager.DiagnosticsGeneration).
// A session already diffed at generation has been shown exactly these
// diagnostics, so DiffAt returns nothing without calling current; otherwise
// it calls current for the diagnostics and diffs them. That spares the
// report built from every server's every diagnostic on the tool calls that
// follow one another with nothing new in between.
func (l *Ledger) DiffAt(session, generation string, current func() map[string]string) (added []string, resolved []ResolvedDiagnostic) {
	if l == nil {
		return nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if seen, ok := l.generations[session]; ok && seen == generation {
		return nil, nil
	}
	added, resolved = l.diffLocked(session, current())
	l.generations[session] = generation
	return added, resolved
}

func (l *Ledger) diffLocked(session string, current map[string]string) (added []string, resolved []ResolvedDiagnostic) {
	previous := l.sessions[session]
	for fingerprint, line := range current {
		if _, seen := previous[fingerprint]; !seen {
			added = append(added, line)
		}
	}
	for fingerprint, line := range previous {
		if _, stillThere := current[fingerprint]; !stillThere {
			resolved = append(resolved, ResolvedDiagnostic{Fingerprint: fingerprint, Line: line})
		}
	}
	l.sessions[session] = maps.Clone(current)

	slices.Sort(added)
	slices.SortFunc(resolved, func(a, b ResolvedDiagnostic) int {
		return strings.Compare(a.Line, b.Line)
	})
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
	delete(l.generations, session)
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
	delete(l.generations, session)
}
