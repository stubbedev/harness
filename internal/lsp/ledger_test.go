package lsp

import (
	"slices"
	"sync"
	"testing"
)

func TestLedgerDiffReportsOnlyWhatIsNew(t *testing.T) {
	t.Parallel()
	l := NewLedger()

	added, resolved := l.Diff("s1", map[string]string{"a": "Error: a", "b": "Warn: b"})
	if !slices.Equal(added, []string{"Error: a", "Warn: b"}) {
		t.Fatalf("first diff added = %v", added)
	}
	if len(resolved) != 0 {
		t.Fatalf("first diff resolved = %v", resolved)
	}

	added, resolved = l.Diff("s1", map[string]string{"a": "Error: a", "b": "Warn: b"})
	if len(added) != 0 || len(resolved) != 0 {
		t.Fatalf("unchanged state reported added=%v resolved=%v", added, resolved)
	}

	added, resolved = l.Diff("s1", map[string]string{"b": "Warn: b", "c": "Error: c"})
	if !slices.Equal(added, []string{"Error: c"}) {
		t.Fatalf("added = %v", added)
	}
	if len(resolved) != 1 || resolved[0].Fingerprint != "a" || resolved[0].Line != "Error: a" {
		t.Fatalf("resolved = %v", resolved)
	}
}

// A diagnostic whose formatted line changed but whose fingerprint did not —
// the same problem after an edit moved it down the file — is not news.
func TestLedgerDiffIgnoresChangedLineForKnownFingerprint(t *testing.T) {
	t.Parallel()
	l := NewLedger()
	l.Diff("s1", map[string]string{"a": "Error: f.go:10:1 boom"})

	added, resolved := l.Diff("s1", map[string]string{"a": "Error: f.go:42:1 boom"})
	if len(added) != 0 || len(resolved) != 0 {
		t.Fatalf("added=%v resolved=%v", added, resolved)
	}
}

func TestLedgerSessionsAreIndependent(t *testing.T) {
	t.Parallel()
	l := NewLedger()
	l.Diff("s1", map[string]string{"a": "Error: a"})

	added, _ := l.Diff("s2", map[string]string{"a": "Error: a"})
	if !slices.Equal(added, []string{"Error: a"}) {
		t.Fatalf("second session added = %v", added)
	}
}

func TestLedgerRecordSuppressesTheNextDiff(t *testing.T) {
	t.Parallel()
	l := NewLedger()
	l.Record("s1", map[string]string{"a": "Error: a"})

	added, resolved := l.Diff("s1", map[string]string{"a": "Error: a"})
	if len(added) != 0 || len(resolved) != 0 {
		t.Fatalf("added=%v resolved=%v", added, resolved)
	}
}

func TestLedgerForgetReportsEverythingAgain(t *testing.T) {
	t.Parallel()
	l := NewLedger()
	l.Diff("s1", map[string]string{"a": "Error: a"})
	l.Forget("s1")

	added, _ := l.Diff("s1", map[string]string{"a": "Error: a"})
	if !slices.Equal(added, []string{"Error: a"}) {
		t.Fatalf("added after forget = %v", added)
	}
}

// The caller's map must not become the ledger's storage: a reporter is free to
// reuse or mutate it after the call.
func TestLedgerDiffCopiesTheSnapshot(t *testing.T) {
	t.Parallel()
	l := NewLedger()
	current := map[string]string{"a": "Error: a"}
	l.Diff("s1", current)
	current["b"] = "Error: b"

	added, _ := l.Diff("s1", map[string]string{"a": "Error: a", "b": "Error: b"})
	if !slices.Equal(added, []string{"Error: b"}) {
		t.Fatalf("added = %v", added)
	}
}

// Tool calls run in parallel, so two reports can reach the ledger at once.
// Exactly one of them must be told about a given diagnostic.
func TestLedgerDiffIsAtomicUnderConcurrency(t *testing.T) {
	t.Parallel()
	l := NewLedger()
	current := map[string]string{"a": "Error: a"}

	const callers = 16
	var (
		mu     sync.Mutex
		claims int
		wg     sync.WaitGroup
	)
	for range callers {
		wg.Go(func() {
			added, _ := l.Diff("s1", current)
			mu.Lock()
			claims += len(added)
			mu.Unlock()
		})
	}
	wg.Wait()

	if claims != 1 {
		t.Fatalf("diagnostic reported %d times, want exactly 1", claims)
	}
}

func TestLedgerNilIsInert(t *testing.T) {
	t.Parallel()
	var l *Ledger
	added, resolved := l.Diff("s1", map[string]string{"a": "Error: a"})
	if added != nil || resolved != nil {
		t.Fatalf("nil ledger returned added=%v resolved=%v", added, resolved)
	}
	l.Record("s1", nil)
	l.Forget("s1")
}
