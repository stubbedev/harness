package extensions

import (
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/fsext"
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
		paths = fsext.ResolveConfigPaths(cfg.Options.ExtensionsPaths, store.ResolverFunc())
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
