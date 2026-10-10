package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/stubbedev/harness/internal/filepathext"

	"github.com/aymanbagabas/go-udiff"
	"github.com/stubbedev/harness/internal/filetracker"
)

var fileLocks [128]sync.Mutex

// fileLockIndex is the stripe of fileLocks that guards path.
func fileLockIndex(path string) int {
	path = filepathext.Key(path)
	h := fnv.New32a()
	_, _ = h.Write([]byte(path))
	return int(h.Sum32() % uint32(len(fileLocks)))
}

func lockFile(path string) func() {
	mu := &fileLocks[fileLockIndex(path)]
	mu.Lock()
	return mu.Unlock
}

// lockFiles takes the edit lock of every path at once, for a change that
// rewrites several files as one (a rename). Stripes are taken in index
// order and each only once: two paths can share a stripe, the mutexes are
// not reentrant, and a fixed order keeps two multi-file lockers from
// deadlocking on each other. Every other caller holds a single stripe, so
// it cannot form a cycle with this one either.
func lockFiles(paths ...string) func() {
	stripes := make([]int, 0, len(paths))
	for _, path := range paths {
		stripes = append(stripes, fileLockIndex(path))
	}
	slices.Sort(stripes)
	stripes = slices.Compact(stripes)
	for _, i := range stripes {
		fileLocks[i].Lock()
	}
	return func() {
		for _, i := range slices.Backward(stripes) {
			fileLocks[i].Unlock()
		}
	}
}

// fileStamp is the identity a file had when its content was read: its
// modification time and size. Under the per-path edit lock, an
// unchanged stamp means unchanged bytes, which is what lets the edit
// path trust the content it already holds instead of reading the file
// back twice more per call. A writer that changes neither within the
// filesystem's timestamp granularity is invisible to every
// mtime-based guard, this one included.
type fileStamp struct {
	modSec  int64
	modNsec int64
	size    int64
}

func stampOf(info os.FileInfo) fileStamp {
	mod := info.ModTime()
	return fileStamp{mod.Unix(), int64(mod.Nanosecond()), info.Size()}
}

func (st fileStamp) matches(info os.FileInfo) bool {
	return st == stampOf(info)
}

func checkFileEvidence(ctx context.Context, tracker filetracker.Service, session, path string, content []byte, ranges []filetracker.Range) error {
	if evidence, ok := tracker.(filetracker.Evidence); ok {
		return evidence.Check(ctx, session, path, content, ranges)
	}
	if tracker == nil {
		return filetracker.ErrUnread
	}
	lastRead := tracker.LastReadTime(ctx, session, path)
	if lastRead.IsZero() {
		return filetracker.ErrUnread
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.ModTime().Truncate(time.Second).After(lastRead) {
		return filetracker.ErrStale
	}
	return nil
}

func conflictEvidence(ctx context.Context, tracker filetracker.Service, session, path string, content []byte, at int, reason error) error {
	at = max(0, min(at, len(content)))
	start := max(0, at-512)
	end := min(len(content), start+2048)
	for start < end && !utf8.RuneStart(content[start]) {
		start++
	}
	for end < len(content) && end > start && !utf8.RuneStart(content[end]) {
		end--
	}
	excerpt := content[start:end]
	if !utf8.Valid(excerpt) {
		return fmt.Errorf("%w\nRead the affected range with view before retrying (non-UTF-8 content)", reason)
	}
	filetracker.Observe(ctx, tracker, session, path, content, []filetracker.Range{{Start: start, End: end}})
	return fmt.Errorf("%w\nCurrent file %q, bytes %d-%d (bounded excerpt; retry explicitly):\n%s", reason, path, start, end, excerpt)
}

// changedRanges returns the ranges of before that changes, its udiff edits
// to the new content, touch. A pure insertion is widened to the bytes on
// either side of it, so inserting still requires having seen where.
func changedRanges(changes []udiff.Edit, beforeLen int) []filetracker.Range {
	var ranges []filetracker.Range
	for _, change := range changes {
		start, end := change.Start, change.End
		if start == end && beforeLen > 0 {
			start = max(0, start-1)
			end = min(beforeLen, end+1)
		}
		ranges = append(ranges, filetracker.Range{Start: start, End: end})
	}
	return ranges
}

func guardedWrite(path string, before, after []byte, create bool) error {
	return guardedWriteStamped(path, before, after, create, fileStamp{})
}

// guardedWriteStamped is guardedWrite with the stamp of the stat that
// went with `before`: a file whose stamp still matches is exactly the
// bytes the caller already checked, so the rename happens without
// reading the file back. Anything else falls back to the comparison.
func guardedWriteStamped(path string, before, after []byte, create bool, stamp fileStamp) error {
	if create {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, err = f.Write(after)
		return errors.Join(err, f.Close())
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".harness-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(info.Mode().Perm()); err != nil {
		f.Close()
		return err
	}
	_, writeErr := f.Write(after)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	if stamp.matches(info) {
		return os.Rename(f.Name(), path)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, before) {
		return filetracker.ErrStale
	}
	return os.Rename(f.Name(), path)
}

func lineRange(content []byte, offset, limit int) filetracker.Range {
	start := 0
	for range max(0, offset) {
		i := bytes.IndexByte(content[start:], '\n')
		if i < 0 {
			return filetracker.Range{Start: len(content), End: len(content)}
		}
		start += i + 1
	}
	end := start
	for range max(0, limit) {
		i := bytes.IndexByte(content[end:], '\n')
		if i < 0 {
			end = len(content)
			break
		}
		end += i + 1
	}
	return filetracker.Range{Start: start, End: end}
}

// seenTextRanges returns the byte ranges of data whose lines a view returned
// verbatim as text, starting at line offset. The returned lines are
// consecutive, so the first one is located once and each following one is
// the next line in data; locating every line with its own lineRange scan
// made a view of a late page cost the whole file once per returned line.
func seenTextRanges(data []byte, offset int, text string) []filetracker.Range {
	var ranges []filetracker.Range
	next := lineRange(data, offset, 0).Start
	for i, line := range strings.Split(text, "\n") {
		// Lines before the start of the file all resolve to line 0, the
		// way lineRange clamps a negative offset.
		r := filetracker.Range{Start: next, End: len(data)}
		if j := bytes.IndexByte(data[next:], '\n'); j >= 0 {
			r.End = next + j + 1
		}
		if offset+i >= 0 {
			next = r.End
		}
		raw := strings.TrimSuffix(strings.TrimSuffix(string(data[r.Start:r.End]), "\n"), "\r")
		if len(raw) > MaxLineLength {
			prefix := strings.ToValidUTF8(raw[:MaxLineLength], "")
			if line == prefix+"..." {
				ranges = append(ranges, filetracker.Range{Start: r.Start, End: r.Start + len(prefix)})
			}
		} else if line == raw {
			ranges = append(ranges, r)
		}
	}
	return ranges
}

type sourceEvidenceKey struct{}

func sourceTracker(ctx context.Context) filetracker.Service {
	tracker, _ := ctx.Value(sourceEvidenceKey{}).(filetracker.Service)
	return tracker
}

func checkEditRanges(edit editContext, norm *normCache, session, path, content string, crlf bool, operations []EditOperation) error {
	if _, ok := edit.filetracker.(filetracker.Evidence); !ok {
		return nil
	}
	raw := content
	if crlf {
		raw = strings.ReplaceAll(content, "\n", "\r\n")
	}
	var ranges []filetracker.Range
	toRaw := func(offset int) int {
		if crlf {
			return offset + strings.Count(content[:offset], "\n")
		}
		return offset
	}
	for _, operation := range operations {
		found := false
		for cursor := 0; cursor <= len(content); {
			i := strings.Index(content[cursor:], operation.OldString)
			if i < 0 || operation.OldString == "" {
				break
			}
			start := cursor + i
			end := start + len(operation.OldString)
			ranges = append(ranges, filetracker.Range{Start: toRaw(start), End: toRaw(end)})
			found = true
			cursor = end
		}
		if !found {
			nc := norm.of(content)
			for _, match := range nc.matches(operation.OldString) {
				r := nc.lineRange(match.startLine, match.endLine)
				ranges = append(ranges, filetracker.Range{Start: toRaw(r.Start), End: toRaw(r.End)})
			}
		}
	}
	if len(ranges) == 0 {
		return nil
	}
	// Every range passing is the common case, and one check of all of them
	// hashes the file once instead of once per range. Only a failure needs
	// the per-range pass, to report the first range that failed.
	rawBytes := []byte(raw)
	if checkFileEvidence(edit.ctx, edit.filetracker, session, path, rawBytes, ranges) == nil {
		return nil
	}
	for _, affected := range ranges {
		err := checkFileEvidence(edit.ctx, edit.filetracker, session, path, rawBytes, []filetracker.Range{affected})
		if err == nil {
			continue
		}
		return conflictEvidence(edit.ctx, edit.filetracker, session, path, rawBytes, affected.Start, withExactMatchHint(err))
	}

	return nil
}

// withExactMatchHint appends the exact-match alternative to an unread
// refusal, since an edit that matches byte-for-byte needs no view.
func withExactMatchHint(err error) error {
	if errors.Is(err, filetracker.ErrUnread) {
		return fmt.Errorf("%w. An edit whose old_string matches the file byte-for-byte applies without a view", err)
	}
	return err
}

// exactMatchEvidence reports whether every operation's old_string matches
// the content byte-for-byte, exactly once. A match like that is its own
// evidence: the replacement runs against bytes read in this call, so it
// cannot corrupt content the model never saw, and it does not need the
// view the certification otherwise asks for. replace_all is excluded on
// purpose - it multiplies matches beyond the model's stated context, so
// it still certifies - and the whitespace-tolerant fallback does not
// count either, since approximately matching unseen text is what the
// view requirement exists to prevent.
func exactMatchEvidence(content string, operations []EditOperation) bool {
	if len(operations) == 0 {
		return false
	}
	for _, operation := range operations {
		if operation.ReplaceAll || strings.Count(content, operation.OldString) != 1 {
			return false
		}
	}
	return true
}

// shellTrackedPathLimit caps how many existing paths one shell command
// gets stat-checked for mutations.
const shellTrackedPathLimit = 64

// shellCommandFiles returns the existing regular files a command line
// names, resolved from workingDir when relative. It is a heuristic by
// construction: redirections written without a space, globs and paths
// built at runtime are invisible to it. Its bias is to miss, which keeps
// today's behaviour; a false positive is near-impossible because the
// mutation check below still requires the file's stamp to move.
func shellCommandFiles(command, workingDir string) []string {
	var paths []string
	for _, token := range strings.FieldsFunc(command, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ';' || r == '&' || r == '|' || r == '(' || r == ')'
	}) {
		token = strings.Trim(token, "\"'`")
		token = strings.TrimLeft(token, "0123456789<>")
		token = strings.TrimRight(token, ",:")
		if token == "" || token == "<<" {
			continue
		}
		path := token
		if !filepath.IsAbs(path) {
			path = filepath.Join(workingDir, path)
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			paths = append(paths, path)
			if len(paths) >= shellTrackedPathLimit {
				break
			}
		}
	}
	return paths
}

// stampPaths stats every path once, as the before side of a mutation
// check. Paths that do not stat are simply absent.
func stampPaths(paths []string) map[string]fileStamp {
	if len(paths) == 0 {
		return nil
	}
	stamps := make(map[string]fileStamp, len(paths))
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil {
			stamps[path] = stampOf(info)
		}
	}
	return stamps
}

// certifyShellMutations observes, for the session, the new state of every
// path a shell command changed: the command line just rewrote them, so
// the next edit matches against bytes the session put there itself and
// needs no fresh view first.
func certifyShellMutations(ctx context.Context, tracker filetracker.Service, session string, before map[string]fileStamp, paths []string) {
	if tracker == nil || len(paths) == 0 {
		return
	}
	evidence, ok := tracker.(filetracker.Evidence)
	if !ok {
		return
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if stamp, seen := before[path]; seen && stamp.matches(info) {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		evidence.Observe(ctx, session, path, content, []filetracker.Range{{Start: 0, End: len(content)}})
	}
}
