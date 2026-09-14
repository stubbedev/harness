package extensions

import (
	"strings"

	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/home"
	"github.com/stubbedev/harness/internal/hooks"
)

// OptionsFromStore builds Host options from the config store: the
// configured extension paths (home- and $VAR-expanded), the disabled
// list, and a hook registry to police privileged host functions.
//
// The gate is a registry of its own rather than the coordinator's. The
// coordinator's registry dispatches into extensions, and a host function
// that fired hooks through it would re-enter the VM it was called from.
// Config-declared shell hooks see extension activity either way.
func OptionsFromStore(store *config.ConfigStore) Options {
	cfg := store.Config()

	var paths, disabled []string
	if cfg != nil && cfg.Options != nil {
		paths = resolvePaths(store, cfg.Options.ExtensionsPaths)
		disabled = cfg.Options.DisabledExtensions
	}

	var dataDir string
	if cfg != nil && cfg.Options != nil {
		dataDir = cfg.Options.DataDirectory
	}

	return Options{
		Paths:      paths,
		Disabled:   disabled,
		WorkingDir: store.WorkingDir(),
		DataDir:    dataDir,
		Gate:       hooks.NewRegistry(store, store.WorkingDir(), store.WorkingDir()),
	}
}

// resolvePaths expands home-directory and $VAR references the same way
// skill paths are expanded.
func resolvePaths(store *config.ConfigStore, paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	resolver := store.Resolver()
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		expanded := home.Long(path)
		if strings.HasPrefix(expanded, "$") && resolver != nil {
			if resolved, err := resolver.ResolveValue(expanded); err == nil {
				expanded = resolved
			}
		}
		out = append(out, expanded)
	}
	return out
}
