package filetracker

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"slices"
	"sync"

	"github.com/aymanbagabas/go-udiff"
)

type Range struct{ Start, End int }

type Evidence interface {
	Observe(context.Context, string, string, []byte, []Range)
	Check(context.Context, string, string, []byte, []Range) error
	Advance(context.Context, string, string, []byte, []byte)
}

var (
	ErrUnread = errors.New("you must read the affected range before editing it. Use the View tool first")
	ErrStale  = errors.New("file has been modified since it was last read; no changes made")
)

type observation struct {
	version [sha256.Size]byte
	ranges  []Range
}

type evidenceStore struct {
	mu   sync.Mutex
	seen map[string]observation
}

func evidenceKey(session, path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return session + "\x00" + filepath.Clean(path)
}

func mergeRanges(ranges []Range) []Range {
	slices.SortFunc(ranges, func(a, b Range) int { return a.Start - b.Start })
	var merged []Range
	for _, r := range ranges {
		if len(merged) > 0 && r.Start <= merged[len(merged)-1].End {
			merged[len(merged)-1].End = max(merged[len(merged)-1].End, r.End)
		} else {
			merged = append(merged, r)
		}
	}
	return merged
}

func (s *service) Observe(ctx context.Context, session, path string, content []byte, ranges []Range) {
	s.evidence.mu.Lock()
	if s.evidence.seen == nil {
		s.evidence.seen = make(map[string]observation)
	}
	key := evidenceKey(session, path)
	version := sha256.Sum256(content)
	obs := s.evidence.seen[key]
	if obs.version != version {
		obs = observation{version: version}
	}
	for _, r := range ranges {
		if r.Start >= 0 && r.End >= r.Start && r.End <= len(content) {
			obs.ranges = append(obs.ranges, r)
		}
	}
	obs.ranges = mergeRanges(obs.ranges)
	s.evidence.seen[key] = obs
	s.evidence.mu.Unlock()
	s.RecordRead(ctx, session, path)
}

func (s *service) Check(_ context.Context, session, path string, content []byte, ranges []Range) error {
	s.evidence.mu.Lock()
	defer s.evidence.mu.Unlock()
	obs, ok := s.evidence.seen[evidenceKey(session, path)]
	if !ok {
		return ErrUnread
	}
	if obs.version != sha256.Sum256(content) {
		return ErrStale
	}
	for _, want := range ranges {
		covered := false
		for _, seen := range obs.ranges {
			if seen.Start <= want.Start && seen.End >= want.End {
				covered = true
				break
			}
		}
		if !covered {
			return ErrUnread
		}
	}
	return nil
}

func (s *service) Advance(ctx context.Context, session, path string, before, after []byte) {
	s.evidence.mu.Lock()
	key := evidenceKey(session, path)
	obs := s.evidence.seen[key]
	var ranges []Range
	changes := udiff.Bytes(before, after)
	if obs.version == sha256.Sum256(before) {
		for _, seen := range obs.ranges {
			cursor, shift := seen.Start, 0
			for _, change := range changes {
				if change.Start >= seen.End {
					break
				}
				if change.End <= cursor {
					shift += len(change.New) - (change.End - change.Start)
					continue
				}
				if change.Start > cursor {
					ranges = append(ranges, Range{cursor + shift, change.Start + shift})
				}
				cursor = min(seen.End, change.End)
				shift += len(change.New) - (change.End - change.Start)
			}
			if cursor < seen.End {
				ranges = append(ranges, Range{cursor + shift, seen.End + shift})
			}
		}
	}
	shift := 0
	for _, change := range changes {
		ranges = append(ranges, Range{change.Start + shift, change.Start + shift + len(change.New)})
		shift += len(change.New) - (change.End - change.Start)
	}
	if s.evidence.seen == nil {
		s.evidence.seen = make(map[string]observation)
	}
	s.evidence.seen[key] = observation{sha256.Sum256(after), mergeRanges(ranges)}
	s.evidence.mu.Unlock()
	s.RecordRead(ctx, session, path)
}

func Observe(ctx context.Context, tracker Service, session, path string, content []byte, ranges []Range) {
	if tracker == nil {
		return
	}
	if evidence, ok := tracker.(Evidence); ok {
		evidence.Observe(ctx, session, path, content, ranges)
	} else {
		tracker.RecordRead(ctx, session, path)
	}
}

func Advance(ctx context.Context, tracker Service, session, path string, before, after []byte) {
	if tracker == nil {
		return
	}
	if evidence, ok := tracker.(Evidence); ok {
		evidence.Advance(ctx, session, path, before, after)
	} else {
		tracker.RecordRead(ctx, session, path)
	}
}
