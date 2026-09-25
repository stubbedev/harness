// Package presence lets harness instances running against the same
// workspace discover each other. Each instance owns one small JSON
// record under <workspace data dir>/presence/ that it rewrites with a
// heartbeat and stamps with the files its agents recently mutated;
// every other instance in the workspace can read those records to see
// who else is live and what they are touching.
//
// The registry directory is the IPC medium. Each record file has
// exactly one writer (its owner), and writes are atomic (temp file +
// rename), so readers always observe a complete record and no locking
// is needed. A record whose last beat is older than StaleAfter belongs
// to an instance that is gone; any instance may delete a record long
// past that mark. An owner whose file was collected simply recreates it
// on its next beat, so garbage collection is self-healing.
//
// Every method is nil-safe: a nil Registry (feature disabled, tests,
// sub-agents) is a no-op, so callers never branch on nil.
package presence

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// DirName is the registry directory, under the workspace data dir.
const DirName = "presence"

const (
	// BeatInterval is how often a live instance rewrites its record.
	BeatInterval = 5 * time.Second

	// StaleAfter is how old a record's last beat may be before the
	// instance is considered gone.
	StaleAfter = 20 * time.Second

	// gcAfter is how old a record must be before another instance may
	// delete its file: well past StaleAfter, so an owner whose beats
	// were merely delayed is never collected underneath itself.
	gcAfter = 10 * time.Minute

	// MaxFiles caps the published recent-activity list.
	MaxFiles = 20

	// ActivityWindow bounds how long a touched file stays published.
	ActivityWindow = 5 * time.Minute

	// warnInterval rate-limits Report per path so repeated edits of a
	// contended file do not stamp every tool result.
	warnInterval = 2 * time.Minute
)

// Activity is one recently mutated file, workspace-relative.
type Activity struct {
	Path string    `json:"path"`
	At   time.Time `json:"at"`
}

// Record is one instance's published state.
type Record struct {
	ID        string     `json:"id"`
	PID       int        `json:"pid"`
	Started   time.Time  `json:"started"`
	Beat      time.Time  `json:"beat"`
	SessionID string     `json:"session_id,omitempty"`
	Title     string     `json:"title,omitempty"`
	Busy      bool       `json:"busy,omitempty"`
	Files     []Activity `json:"files,omitempty"`
}

// Registry publishes this instance's record and reads the records of
// every other instance in the same workspace data directory. The zero
// value is not usable; call New. All methods tolerate a nil Registry.
type Registry struct {
	dir string

	mu   sync.Mutex
	self Record
	// turns counts concurrent agent turns; Busy is turns > 0.
	turns int
	// warned remembers when Report last warned about each path.
	warned map[string]time.Time

	stop    chan struct{}
	stopped sync.Once

	// The fields below are swappable for tests; production uses the
	// package constants.
	now            func() time.Time
	beatInterval   time.Duration
	staleAfter     time.Duration
	gcThreshold    time.Duration
	activityWindow time.Duration
}

// New creates the registry directory and a registry for one instance.
// The record is not published until Start.
func New(dataDir string) (*Registry, error) {
	dir := filepath.Join(dataDir, DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating presence directory: %w", err)
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	return &Registry{
		dir:            dir,
		self:           Record{ID: id, PID: os.Getpid(), Started: time.Now()},
		warned:         make(map[string]time.Time),
		stop:           make(chan struct{}),
		now:            time.Now,
		beatInterval:   BeatInterval,
		staleAfter:     StaleAfter,
		gcThreshold:    gcAfter,
		activityWindow: ActivityWindow,
	}, nil
}

func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating presence id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// Start publishes the initial record and heartbeats until ctx is
// cancelled or Stop is called. Stop removes the record itself; ctx
// cancellation hands that to the loop so an owner that never stops
// explicitly still retires.
func (r *Registry) Start(ctx context.Context) {
	if r == nil {
		return
	}
	r.beat()
	go func() {
		ticker := time.NewTicker(r.beatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				r.removeSelf()
				return
			case <-r.stop:
				return
			case <-ticker.C:
				r.beat()
			}
		}
	}()
}

// Stop removes this instance's record and stops heartbeating. It is
// idempotent and safe to call alongside context cancellation: whoever
// retires the record first wins and the other becomes a no-op.
func (r *Registry) Stop() {
	if r == nil {
		return
	}
	r.stopped.Do(func() {
		close(r.stop)
		r.removeSelf()
	})
}

func (r *Registry) removeSelf() {
	if err := os.Remove(r.path(r.Self().ID)); err != nil && !os.IsNotExist(err) {
		slog.Debug("Failed to remove presence record", "error", err)
	}
}

// SetSession stamps the session this instance is currently working on.
func (r *Registry) SetSession(sessionID, title string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.self.SessionID = sessionID
	r.self.Title = title
	r.mu.Unlock()
	r.beat()
}

// BeginTurn marks the instance's agent as mid-turn. Nested and parallel
// turns are counted; the mark clears when the last one ends.
func (r *Registry) BeginTurn() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.turns++
	r.self.Busy = true
	r.mu.Unlock()
	r.beat()
}

// EndTurn releases one BeginTurn mark.
func (r *Registry) EndTurn() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.turns > 0 {
		r.turns--
	}
	r.self.Busy = r.turns > 0
	r.mu.Unlock()
	r.beat()
}

// Report is the single chokepoint for tool-driven mutations: it records
// that this instance wrote path and returns a warning line when a live
// peer wrote it within the activity window, or "". The warning is
// rate-limited per path so repeated edits of a contended file do not
// stamp every tool result. It is a soft note, never a refusal; the
// file-evidence layer remains the hard line for stale writes.
func (r *Registry) Report(path string) string {
	if r == nil {
		return ""
	}
	r.recordActivity(path)
	return r.contention(path)
}

// recordActivity stamps path into the recent-activity list, stored
// workspace-relative.
func (r *Registry) recordActivity(path string) {
	r.mu.Lock()
	now := r.now()
	if rel := relative(path); rel != "" {
		r.self.Files = append(r.self.Files, Activity{Path: rel, At: now})
		r.self.Files = trimActivity(r.self.Files, now, r.activityWindow)
		if len(r.self.Files) > MaxFiles {
			r.self.Files = r.self.Files[:MaxFiles]
		}
	}
	r.mu.Unlock()
	r.beat()
}

// Self returns a copy of this instance's record.
func (r *Registry) Self() Record {
	if r == nil {
		return Record{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.self
	rec.Files = slices.Clone(r.self.Files)
	return rec
}

// Peers returns the live records of every other instance in this
// workspace, oldest start first, with file activity trimmed to the
// activity window. Records long past their beat are collected.
func (r *Registry) Peers() []Record {
	if r == nil {
		return nil
	}
	self := r.Self()
	now := r.now()
	peers := r.scan(self.ID, now, true)
	slices.SortFunc(peers, func(a, b Record) int {
		switch {
		case a.Started.Before(b.Started):
			return -1
		case a.Started.After(b.Started):
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	return peers
}

// contention returns the rate-limited warning for path when a live peer
// wrote it recently, or "".
func (r *Registry) contention(path string) string {
	rel := relative(path)
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for p, t := range r.warned {
		if now.Sub(t) > warnInterval {
			delete(r.warned, p)
		}
	}
	var peer Record
	var latest time.Time
	found := false
	for _, rec := range r.scan(r.self.ID, now, false) {
		for _, a := range rec.Files {
			if a.Path == rel && (!found || a.At.After(latest)) {
				peer, latest, found = rec, a.At, true
			}
		}
	}
	if !found || now.Sub(latest) > r.activityWindow {
		return ""
	}
	if last, ok := r.warned[rel]; ok && now.Sub(last) < warnInterval {
		return ""
	}
	r.warned[rel] = now
	who := fmt.Sprintf("pid %d", peer.PID)
	if peer.Title != "" {
		who += fmt.Sprintf(" (%q)", peer.Title)
	}
	return fmt.Sprintf(
		"Note: another harness instance in this workspace (%s) wrote %s %s ago; re-read it before relying on its contents.",
		who, rel, now.Sub(latest).Truncate(time.Second),
	)
}

// scan reads every record in the registry directory except selfID,
// skipping instances past staleAfter and collecting the long dead. It
// takes no locks, so it is safe under mu from contention.
func (r *Registry) scan(selfID string, now time.Time, collect bool) []Record {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil
	}
	var peers []Record
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(r.dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var rec Record
		if json.Unmarshal(data, &rec) != nil || rec.ID == "" {
			if collect {
				r.collect(path, entry, now)
			}
			continue
		}
		if rec.ID == selfID {
			continue
		}
		if age := now.Sub(rec.Beat); age > r.gcThreshold {
			// The record itself dates the beat, so collect it outright;
			// only an undatable file needs the modtime fallback.
			if collect {
				_ = os.Remove(path)
			}
			continue
		} else if age > r.staleAfter {
			continue
		}
		rec.Files = trimActivity(rec.Files, now, r.activityWindow)
		peers = append(peers, rec)
	}
	return peers
}

// collect removes a file that cannot be dated by its content; its
// modtime decides whether it is garbage or someone else's fresh write.
func (r *Registry) collect(path string, entry os.DirEntry, now time.Time) {
	info, err := entry.Info()
	if err == nil && now.Sub(info.ModTime()) < r.gcThreshold {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Debug("Failed to collect stale presence record", "file", path, "error", err)
	}
}

// beat atomically rewrites this instance's record with a fresh beat.
// After Stop it is a no-op, so a late mutator cannot resurrect a
// retired record.
func (r *Registry) beat() {
	rec := r.Self()
	select {
	case <-r.stop:
		return
	default:
	}
	rec.Beat = r.now()
	data, err := json.Marshal(rec)
	if err != nil {
		slog.Debug("Failed to encode presence record", "error", err)
		return
	}
	tmp, err := os.CreateTemp(r.dir, ".beat-*")
	if err != nil {
		slog.Debug("Failed to write presence record", "error", err)
		return
	}
	name := tmp.Name()
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(name)
		slog.Debug("Failed to write presence record", "error", err)
		return
	}
	if err := os.Rename(name, r.path(rec.ID)); err != nil {
		_ = os.Remove(name)
		slog.Debug("Failed to publish presence record", "error", err)
	}
}

func (r *Registry) path(id string) string {
	return filepath.Join(r.dir, id+".json")
}

// trimActivity drops entries outside window and keeps the most recent
// entry per path, most recent first.
func trimActivity(files []Activity, now time.Time, window time.Duration) []Activity {
	files = slices.DeleteFunc(files, func(a Activity) bool {
		return now.Sub(a.At) > window
	})
	slices.SortFunc(files, func(a, b Activity) int {
		switch {
		case a.At.After(b.At):
			return -1
		case a.At.Before(b.At):
			return 1
		}
		return strings.Compare(a.Path, b.Path)
	})
	return slices.CompactFunc(files, func(a, b Activity) bool {
		return a.Path == b.Path
	})
}

// relative flattens a path to workspace-relative slash form, the same
// way filetracker stores its session rows.
func relative(path string) string {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return filepath.ToSlash(path)
	}
	wd, err := os.Getwd()
	if err != nil {
		return filepath.ToSlash(path)
	}
	rel, err := filepath.Rel(wd, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}
