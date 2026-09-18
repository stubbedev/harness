package model

import (
	"context"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fsnotify/fsnotify"
	"github.com/stubbedev/harness/internal/message"
)

const (
	// gitWatchDebounce coalesces filesystem events and explicit
	// invalidations into a single git invocation: a bash loop touching
	// the tree must not mean one git status per write.
	gitWatchDebounce = 300 * time.Millisecond
	// gitWatchBackstop is the periodic re-check for changes nothing
	// watches. Worktree edits from another terminal never touch .git,
	// so watching the git directory alone cannot see them; this bounds
	// how long they can stay invisible. While the repository stays
	// quiet the interval backs off to gitWatchBackstopMax, and any
	// filesystem event or poke resets it, so an idle session trends
	// towards a handful of git invocations per hour.
	gitWatchBackstop    = 15 * time.Second
	gitWatchBackstopMax = 60 * time.Second
	// gitWatchMaxWatchDirs caps how many directories the refs walk
	// watches. Repositories with a pathological number of namespaced
	// branch directories must not translate into thousands of watch
	// descriptors; the walk order puts refs/heads and refs/remotes
	// first, and anything cut off is still covered by the backstop.
	gitWatchMaxWatchDirs = 64
)

// gitStatusChangedMsg is delivered when a background refresh produced a
// different summary than the cached one, so the header repaints itself
// instead of waiting to be drawn for unrelated reasons.
type gitStatusChangedMsg struct{}

// waitGitStatusChanged blocks until the git watcher reports a change.
// Init starts one instance and the gitStatusChangedMsg case re-arms it,
// so exactly one is in flight for the life of the program.
func waitGitStatusChanged() tea.Msg {
	<-gitWatch.changed
	return gitStatusChangedMsg{}
}

// gitCacheEntry is the cached summary string for one working directory.
type gitCacheEntry struct {
	value string
}

// gitWatcher keeps the git segment of the compact status header current
// without any polling on the draw path. It watches the repository's git
// directory (HEAD, index and the refs tree) so branch switches, commits,
// staging and fetches show up immediately - including ones made from
// another terminal - and coalesces everything into at most one git
// invocation per gitWatchDebounce window. Events that arrive while a
// refresh is running are dropped: git status opportunistically rewrites
// the index, and treating our own writes as invalidations would ping-pong
// forever. A backing-off backstop timer catches worktree changes that
// never touch .git. The steady-state cost is a handful of watch
// descriptors and one parked goroutine.
type gitWatcher struct {
	changed chan struct{}

	// mu guards cache and targetDir. Everything below it is owned by
	// the run goroutine.
	mu        sync.Mutex
	cache     map[string]*gitCacheEntry
	targetDir string

	retarget chan struct{}
	poke     chan struct{}
	resolved chan gitDirs
	done     chan struct{}
	started  sync.Once

	fw        *fsnotify.Watcher
	watchDirs []string
	target    string
	resolving bool
	updating  bool
	dirty     bool
	// quiet and backoff drive the backstop: quiet means no filesystem
	// event or poke arrived since the previous backstop fire, so the
	// interval is allowed to grow.
	quiet   bool
	backoff time.Duration
}

// gitDirs is the resolved location of a working directory's git state.
type gitDirs struct {
	dir       string
	gitDir    string
	commonDir string
}

var gitWatch = &gitWatcher{
	changed:  make(chan struct{}, 1),
	retarget: make(chan struct{}, 1),
	poke:     make(chan struct{}, 1),
	resolved: make(chan gitDirs, 1),
	done:     make(chan struct{}, 1),
}

// status returns the cached summary for dir, adopting dir as the
// watched directory on first sight or when it changes (a workspace
// switch gets its own cache entry, never the previous workspace's
// summary). It never runs git and never blocks.
func (w *gitWatcher) status(dir string) string {
	if dir == "" {
		return ""
	}
	w.start()
	w.mu.Lock()
	entry, ok := w.cache[dir]
	if !ok {
		entry = &gitCacheEntry{}
		if w.cache == nil {
			w.cache = make(map[string]*gitCacheEntry)
		}
		w.cache[dir] = entry
	}
	value := entry.value
	retarget := w.targetDir != dir
	if retarget {
		w.targetDir = dir
	}
	w.mu.Unlock()
	if retarget {
		select {
		case w.retarget <- struct{}{}:
		default:
		}
	}
	return value
}

// pokeSoon asks for a re-check of the watched directory. The channel is
// a wakeup, not a queue: the loop re-reads the authoritative target.
func (w *gitWatcher) pokeSoon() {
	w.start()
	select {
	case w.poke <- struct{}{}:
	default:
	}
}

// start lazily creates the fsnotify watcher and the run goroutine, so
// processes that never draw the header (tests, non-TUI commands) pay
// nothing.
func (w *gitWatcher) start() {
	w.started.Do(func() {
		fw, err := fsnotify.NewWatcher()
		if err != nil {
			// Watching is an optimization; pokes and the backstop
			// timer keep the segment correct without it.
			slog.Debug("Git segment: filesystem watching unavailable, falling back to periodic refresh", "error", err)
		}
		w.fw = fw
		go w.run()
	})
}

// run is the watcher's event loop. It owns every field it touches; the
// outside world only ever sends on the buffered channels.
func (w *gitWatcher) run() {
	var events <-chan fsnotify.Event
	var errs <-chan error
	if w.fw != nil {
		events = w.fw.Events
		errs = w.fw.Errors
	}

	debounce := time.NewTimer(time.Hour)
	if !debounce.Stop() {
		<-debounce.C
	}
	debounced := false
	schedule := func() {
		if w.target == "" {
			return
		}
		w.dirty = true
		if !debounced {
			debounced = true
			debounce.Reset(gitWatchDebounce)
		}
	}
	tick := time.NewTimer(gitWatchBackstop)
	defer tick.Stop()
	w.backoff = gitWatchBackstop

	for {
		select {
		case <-w.retarget:
			w.mu.Lock()
			dir := w.targetDir
			w.mu.Unlock()
			if dir == "" || dir == w.target {
				continue
			}
			w.target = dir
			w.quiet = false
			if w.fw != nil && !w.resolving {
				w.resolving = true
				go func() { w.resolved <- resolveGitDirs(dir) }()
			}
			schedule()
		case r := <-w.resolved:
			w.resolving = false
			if r.dir == w.target {
				w.applyWatches(r.gitDir, r.commonDir)
			} else if w.target != "" {
				// A retarget landed while this resolve was in
				// flight: resolve again for the new target.
				w.resolving = true
				go func() { w.resolved <- resolveGitDirs(w.target) }()
			}
		case <-w.poke:
			w.quiet = false
			schedule()
		case <-tick.C:
			if w.quiet {
				w.backoff = min(w.backoff*2, gitWatchBackstopMax)
			} else {
				w.backoff = gitWatchBackstop
			}
			w.quiet = true
			tick.Reset(w.backoff)
			schedule()
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if ev.Has(fsnotify.Chmod) {
				continue
			}
			if w.updating {
				// Most likely our own git status rewriting the
				// index; see the type comment.
				continue
			}
			w.quiet = false
			schedule()
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			slog.Debug("Git segment: watch error", "error", err)
		case <-debounce.C:
			debounced = false
			if w.updating || w.target == "" {
				continue
			}
			w.dirty = false
			w.updating = true
			go func(dir string) {
				info := collectGitInfo(dir)
				w.store(dir, info)
				w.done <- struct{}{}
			}(w.target)
		case <-w.done:
			w.updating = false
			if w.dirty {
				schedule()
			}
		}
	}
}

// store writes a refreshed summary into the cache for dir and wakes the
// UI when it differs from what is on screen. The unchanged guard keeps
// a quiet tree or a wedged repo costing nothing.
func (w *gitWatcher) store(dir, info string) {
	w.mu.Lock()
	entry, ok := w.cache[dir]
	if !ok {
		entry = &gitCacheEntry{}
		if w.cache == nil {
			w.cache = make(map[string]*gitCacheEntry)
		}
		w.cache[dir] = entry
	}
	changed := entry.value != info
	entry.value = info
	w.mu.Unlock()
	if changed {
		select {
		case w.changed <- struct{}{}:
		default:
		}
	}
}

// applyWatches swaps the watched set to the given git directories: the
// directory roots see HEAD, index and packed-refs writes, the refs trees
// (tiny by construction) see branch tip updates.
func (w *gitWatcher) applyWatches(gitDir, commonDir string) {
	if w.fw == nil {
		return
	}
	for _, d := range w.watchDirs {
		_ = w.fw.Remove(d)
	}
	w.watchDirs = w.watchDirs[:0]
	seen := make(map[string]bool)
	for _, root := range []string{gitDir, commonDir} {
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		w.addWatch(root)
		refs := filepath.Join(root, "refs")
		_ = filepath.WalkDir(refs, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				w.addWatch(path)
				if len(w.watchDirs) >= gitWatchMaxWatchDirs {
					return fs.SkipAll
				}
			}
			return nil
		})
	}
}

func (w *gitWatcher) addWatch(dir string) {
	if err := w.fw.Add(dir); err != nil {
		// An unwatchable repo still gets pokes and the backstop.
		slog.Debug("Git segment: cannot watch directory", "dir", dir, "error", err)
		return
	}
	w.watchDirs = append(w.watchDirs, dir)
}

// hasToolResult reports whether a message carries any finished tool
// call, the signal the watcher uses to notice agent-driven tree changes
// that never touch the git directory (plain worktree edits).
func hasToolResult(msg message.Message) bool {
	return message.HasPart[message.ToolResult](&msg)
}

// resolveGitDirs locates a working directory's git directory and, for
// linked worktrees, the shared common directory holding the refs. Not a
// repository yields empty paths.
func resolveGitDirs(dir string) gitDirs {
	ctx, cancel := context.WithTimeout(context.Background(), gitInfoTimeout)
	defer cancel()
	out, err := gitOutput(ctx, dir, "rev-parse", "--absolute-git-dir", "--git-common-dir")
	if err != nil {
		return gitDirs{dir: dir}
	}
	d := gitDirs{dir: dir}
	for i, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch i {
		case 0:
			d.gitDir = line
		case 1:
			d.commonDir = line
		}
	}
	// --git-common-dir is relative when run from the main worktree.
	if d.commonDir != "" && !filepath.IsAbs(d.commonDir) {
		d.commonDir = filepath.Join(dir, d.commonDir)
	}
	return d
}
