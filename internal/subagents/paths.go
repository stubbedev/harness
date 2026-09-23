package subagents

import (
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/filepathext"
)

// InGlobalDir reports whether path lies inside one of the global (user-scope)
// subagents directories. Anything else — project directories, monorepo roots,
// custom subagents_paths — can arrive with a cloned repository, so callers use
// this to gate trust-sensitive operations: deleting a definition file and
// honoring bypassPermissions without a prompt.
func InGlobalDir(path string) bool {
	if path == "" {
		return false
	}
	for _, dir := range config.GlobalSubagentsDirs() {
		if filepathext.Within(dir, path) {
			return true
		}
	}
	return false
}
