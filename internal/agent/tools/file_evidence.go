package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aymanbagabas/go-udiff"
	"github.com/stubbedev/harness/internal/filetracker"
)

var fileLocks [128]sync.Mutex

func lockFile(path string) func() {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(path))
	mu := &fileLocks[h.Sum32()%uint32(len(fileLocks))]
	mu.Lock()
	return mu.Unlock
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
		if err := checkFileEvidence(edit.ctx, edit.filetracker, session, path, rawBytes, []filetracker.Range{affected}); err != nil {
			return conflictEvidence(edit.ctx, edit.filetracker, session, path, rawBytes, affected.Start, err)
		}
	}

	return nil
}
