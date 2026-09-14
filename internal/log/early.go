package log

import (
	"context"
	"log/slog"
	"sync"
)

// earlyLimit bounds how many records are held before the real logger
// exists. Startup produces a handful; the cap only matters if Setup is
// never reached, and then the process is not logging anyway.
const earlyLimit = 500

// earlyBuffer is the handler installed before the log file is known.
// Config loading resolves providers -- and warns about the ones it
// skips, renames or cannot reach -- before the data directory that
// decides the log path exists, so those records used to be discarded.
// They are held here instead and replayed into the real handler.
type earlyBuffer struct {
	mu      sync.Mutex
	records []slog.Record
	attrs   []slog.Attr
	group   string
}

var early = &earlyBuffer{}

// Early returns the handler to install before Setup runs. Everything it
// receives is replayed once the real handler is in place, so nothing
// logged during startup is lost.
func Early() slog.Handler { return early }

func (b *earlyBuffer) Enabled(context.Context, slog.Level) bool {
	// The level filter belongs to the real handler; whether these
	// records are worth printing is not known yet.
	return true
}

func (b *earlyBuffer) Handle(_ context.Context, r slog.Record) error {
	rec := r.Clone()
	if len(b.attrs) > 0 {
		rec.AddAttrs(b.attrs...)
	}

	early.mu.Lock()
	defer early.mu.Unlock()
	if len(early.records) >= earlyLimit {
		return nil
	}
	early.records = append(early.records, rec)
	return nil
}

func (b *earlyBuffer) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return b
	}
	next := &earlyBuffer{group: b.group}
	next.attrs = append(append([]slog.Attr{}, b.attrs...), attrs...)
	return next
}

func (b *earlyBuffer) WithGroup(name string) slog.Handler {
	if name == "" {
		return b
	}
	next := &earlyBuffer{group: name}
	next.attrs = append([]slog.Attr{}, b.attrs...)
	return next
}

// replay hands everything buffered to the handler that took over, and
// drops it. Records that the new handler filters out (debug records
// without --debug) are skipped, keeping the replay indistinguishable
// from having logged there in the first place.
func replay(h slog.Handler) {
	early.mu.Lock()
	records := early.records
	early.records = nil
	early.mu.Unlock()

	ctx := context.Background()
	for _, r := range records {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		_ = h.Handle(ctx, r)
	}
}
