// Package discovery walks directories for definition files, such as skills
// and subagents, and reduces what it finds by name. The packages that own
// each file format supply the matching and the parsing; this package owns
// the walk, the per-file outcome record, and the precedence rules, so
// every kind of definition resolves name collisions the same way.
package discovery

import (
	"cmp"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/charlievieth/fastwalk"
)

// Outcome is the result of discovering a single definition file.
type Outcome int

const (
	// StateNormal indicates the file was parsed and validated successfully.
	StateNormal Outcome = iota
	// StateError indicates discovery hit a scan, parse or validate error.
	StateError
)

// State records the latest discovery outcome of one definition file.
type State struct {
	Name  string
	Path  string
	State Outcome
	Err   error
}

// Walk loads every file under paths that match accepts. load parses and
// validates one file and returns the item and its name; when it fails, a
// non-empty name marks a file that parsed but did not validate.
//
// Results preserve the caller's path order: everything found under
// paths[0], sorted by file path, then paths[1], and so on. Dedupe and
// DedupeStates keep the last occurrence of a name, so this ordering is what
// makes later paths override earlier ones on a collision. Sorting across
// bases instead would let alphabetical path order decide precedence.
//
// kind names the definition type in log lines.
func Walk[T any](
	kind string,
	paths []string,
	match func(fileName string) bool,
	load func(path string) (T, string, error),
) ([]T, []*State) {
	type hit struct {
		path string
		item T
	}
	var (
		items  []T
		states []*State
		mu     sync.Mutex
		seen   = make(map[string]bool)
	)

	for _, base := range paths {
		var (
			baseHits   []hit
			baseStates []*State
		)
		addState := func(name, path string, outcome Outcome, err error) {
			mu.Lock()
			baseStates = append(baseStates, &State{Name: name, Path: path, State: outcome, Err: err})
			mu.Unlock()
		}
		// fastwalk with Follow is used instead of filepath.WalkDir because
		// WalkDir does not follow symlinked directories below the entry
		// point. fastwalk is concurrent, so shared state goes through mu.
		conf := fastwalk.Config{
			Follow:  true,
			ToSlash: fastwalk.DefaultToSlash(),
		}
		err := fastwalk.Walk(&conf, base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				slog.Warn("Failed to walk definition path entry", "kind", kind, "base", base, "path", path, "error", err)
				addState("", path, StateError, err)
				return nil
			}
			if d.IsDir() || !match(d.Name()) {
				return nil
			}
			mu.Lock()
			if seen[path] {
				mu.Unlock()
				return nil
			}
			seen[path] = true
			mu.Unlock()

			item, name, err := load(path)
			if err != nil {
				if name == "" {
					slog.Warn("Failed to parse definition file", "kind", kind, "path", path, "error", err)
				} else {
					slog.Warn("Definition validation failed", "kind", kind, "path", path, "error", err)
				}
				addState(name, path, StateError, err)
				return nil
			}
			slog.Debug("Loaded definition", "kind", kind, "name", name, "path", path)
			mu.Lock()
			baseHits = append(baseHits, hit{path: path, item: item})
			mu.Unlock()
			addState(name, path, StateNormal, nil)
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			slog.Warn("Failed to walk definition path", "kind", kind, "path", base, "error", err)
		}

		// fastwalk order is non-deterministic, so each base is sorted for
		// stable output. Items and states share one schedule so Dedupe and
		// DedupeStates agree on which occurrence of a name is last.
		slices.SortStableFunc(baseHits, func(a, b hit) int {
			return comparePaths(a.path, b.path)
		})
		slices.SortStableFunc(baseStates, func(a, b *State) int {
			return comparePaths(a.Path, b.Path)
		})
		for _, h := range baseHits {
			items = append(items, h.item)
		}
		states = append(states, baseStates...)
	}
	return items, states
}

func comparePaths(a, b string) int {
	return cmp.Compare(strings.ToLower(a), strings.ToLower(b))
}

// SortStates orders states by path, case-insensitively.
func SortStates(states []*State) {
	slices.SortStableFunc(states, func(a, b *State) int {
		return comparePaths(a.Path, b.Path)
	})
}

// Dedupe removes items that share a name. The last occurrence wins, so a
// definition from a later path, or a user definition appended after a
// builtin, overrides the earlier one.
func Dedupe[T any](all []T, name func(T) string) []T {
	if len(all) == 0 {
		return nil
	}
	last := make(map[string]int, len(all))
	for i, item := range all {
		last[name(item)] = i
	}
	result := make([]T, 0, len(last))
	for i, item := range all {
		if last[name(item)] == i {
			result = append(result, item)
		}
	}
	return result
}

// DedupeStates removes states that share a name, keeping the last one, so
// the surviving state describes the file whose item survived Dedupe.
//
// Error states are exempt. They are per-file diagnostics (paths are
// unique, names are not), and collapsing them by name would let a valid
// definition elsewhere hide the broken file the user is trying to fix.
func DedupeStates(all []*State) []*State {
	last := make(map[string]int, len(all))
	for i, s := range all {
		if s.Name != "" && s.State != StateError {
			last[s.Name] = i
		}
	}
	result := make([]*State, 0, len(all))
	for i, s := range all {
		if s.Name == "" || s.State == StateError || last[s.Name] == i {
			result = append(result, s)
		}
	}
	return result
}

// Filter removes items whose names appear in disabled.
func Filter[T any](all []T, disabled []string, name func(T) string) []T {
	if len(disabled) == 0 {
		return all
	}
	result := make([]T, 0, len(all))
	for _, item := range all {
		if !slices.Contains(disabled, name(item)) {
			result = append(result, item)
		}
	}
	return result
}

// CloneStates returns a deep copy of states so callers cannot mutate the
// source.
func CloneStates(states []*State) []*State {
	if states == nil {
		return nil
	}
	result := make([]*State, len(states))
	for i, s := range states {
		clone := *s
		result[i] = &clone
	}
	return result
}
