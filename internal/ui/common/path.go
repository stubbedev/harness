package common

import (
	"path/filepath"
	"strings"

	"github.com/stubbedev/harness/internal/home"
)

// ShortPath renders p for display: relative to base when p lies inside
// it, and home-shortened otherwise. The base is the workspace root, so a
// file the agent touched reads as the path the user would type from
// there. It is display only - the result must never be handed back to a
// tool as a path.
func ShortPath(base, p string) string {
	if base == "" || !filepath.IsAbs(p) {
		return home.Short(p)
	}
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(p))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return home.Short(p)
	}
	return rel
}
