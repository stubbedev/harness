// Package extensions runs user-authored Lua extensions in an embedded,
// sandboxed VM. An extension registers agent tools, hook handlers and
// slash commands through the `harness` table the host installs; nothing
// else is reachable from the VM except the host functions registered
// here, so the API surface an extension sees is auditable in one place.
package extensions

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// EntryFileName is the file each extension directory must contain. It is
// the only file the host loads; anything else an extension ships is
// reachable through require.
const EntryFileName = "init.lua"

// MaxNameLength bounds an extension name, which doubles as a command and
// tool-name prefix.
const MaxNameLength = 64

var namePattern = regexp.MustCompile(`^[\p{L}\p{N}]+([_-][\p{L}\p{N}]+)*$`)

// Extension is a discovered extension directory, before it is loaded.
type Extension struct {
	// Name is the directory's base name.
	Name string `json:"name"`
	// Dir is the extension's root directory.
	Dir string `json:"dir"`
	// EntryFile is the absolute path of the init.lua that is loaded.
	EntryFile string `json:"entry_file"`
}

// DiscoveryState reports the outcome of discovering or loading one
// extension.
type DiscoveryState int

const (
	// StateNormal means the extension was discovered and loaded.
	StateNormal DiscoveryState = iota
	// StateError means discovery or loading failed.
	StateError
	// StateDisabled means the extension was found but is disabled in the
	// config.
	StateDisabled
)

// State is the latest discovery/load status of one extension.
type State struct {
	Name  string
	Path  string
	State DiscoveryState
	Err   error
}

// ValidateName reports whether a name is usable as an extension name.
// The rules match the skill-name rules: letters and digits, separated by
// single dashes or underscores.
func ValidateName(name string) bool {
	if name == "" || len(name) > MaxNameLength {
		return false
	}
	return namePattern.MatchString(name)
}

// Discover walks every path and returns the extension directories found
// in them, along with a state entry per candidate. Later paths win over
// earlier ones for the same name, so project extensions override global
// ones exactly as skills do.
func Discover(paths []string) ([]*Extension, []*State) {
	var (
		found  []*Extension
		states []*State
	)
	for _, base := range paths {
		entries, err := os.ReadDir(base)
		if err != nil {
			if !os.IsNotExist(err) {
				slog.Warn("Failed to read extensions path", "path", base, "error", err)
				states = append(states, &State{Path: base, State: StateError, Err: err})
			}
			continue
		}
		for _, entry := range entries {
			dir := filepath.Join(base, entry.Name())
			if !isDir(dir) {
				continue
			}
			file := filepath.Join(dir, EntryFileName)
			if _, err := os.Stat(file); err != nil {
				continue
			}
			name := entry.Name()
			if !ValidateName(name) {
				err := errInvalidName(name)
				slog.Warn("Skipping extension with invalid name", "name", name, "path", dir)
				states = append(states, &State{Name: name, Path: file, State: StateError, Err: err})
				continue
			}
			found = append(found, &Extension{Name: name, Dir: dir, EntryFile: file})
		}
	}
	return dedupe(found), states
}

// isDir reports whether path is a directory, following symlinks so an
// extension can be symlinked into a search path.
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// dedupe keeps the last occurrence of each name, so a project extension
// shadows a global one.
func dedupe(all []*Extension) []*Extension {
	last := make(map[string]int, len(all))
	for i, e := range all {
		last[e.Name] = i
	}
	result := make([]*Extension, 0, len(last))
	for i, e := range all {
		if last[e.Name] == i {
			result = append(result, e)
		}
	}
	slices.SortFunc(result, func(a, b *Extension) int {
		return strings.Compare(a.Name, b.Name)
	})
	return result
}

// Filter removes extensions whose name appears in disabled, returning
// the remaining ones and a state entry for each one removed.
func Filter(all []*Extension, disabled []string) ([]*Extension, []*State) {
	if len(disabled) == 0 {
		return all, nil
	}
	var (
		kept   []*Extension
		states []*State
	)
	for _, e := range all {
		if slices.Contains(disabled, e.Name) {
			states = append(states, &State{Name: e.Name, Path: e.EntryFile, State: StateDisabled})
			continue
		}
		kept = append(kept, e)
	}
	return kept, states
}

// errInvalidName builds the error recorded for a directory whose name
// cannot be used as an extension name.
func errInvalidName(name string) error {
	return &InvalidNameError{Name: name}
}

// InvalidNameError is returned for an extension directory whose name is
// not a valid extension name.
type InvalidNameError struct{ Name string }

func (e *InvalidNameError) Error() string {
	return "invalid extension name " + strconv.Quote(e.Name) +
		": use letters and digits separated by single dashes or underscores"
}
