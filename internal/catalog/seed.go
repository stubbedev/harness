package catalog

import (
	"embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
)

//go:embed seed/providers.json
var seedFS embed.FS

// seed holds the boot-time fallback catalog. It is a snapshot of the
// provider and model data bundled at release time and is only used when
// the database has no cached catalog yet (first run, offline) and a
// live fetch fails.
var (
	seedOnce    sync.Once
	seedCatalog []Provider
)

// seedProviders parses and caches the embedded seed catalog. It is
// safe for concurrent use: config loading and tests may both reach it
// in parallel.
func seedProviders() []Provider {
	seedOnce.Do(func() {
		data, err := seedFS.ReadFile("seed/providers.json")
		if err != nil {
			// The seed is embedded; a read failure is a build error,
			// not a runtime condition. Return an empty catalog rather
			// than panic.
			return
		}
		if err := json.Unmarshal(data, &seedCatalog); err != nil {
			seedCatalog = nil
		}
		for i := range seedCatalog {
			sortModels(seedCatalog[i].Models)
		}
	})
	return seedCatalog
}

// Embedded returns the bundled seed catalog. It is the last-resort
// fallback when no database cache and no live fetch are available, and
// doubles as the source for "update-providers embedded".
func Embedded() []Provider {
	return seedProviders()
}

// SeedProvider returns a single seed provider by id.
func SeedProvider(id InferenceProvider) (Provider, error) {
	for _, p := range seedProviders() {
		if p.ID == id {
			return p, nil
		}
	}
	return Provider{}, fmt.Errorf("no embedded seed provider %q", id)
}

// ParseProviders decodes a provider catalog from raw JSON. It accepts
// both shapes harness understands: the models.dev api.json document
// (an object keyed by provider id) and a plain array of providers, as
// written by the local cache or hand-maintained files. The models.dev
// shape is translated through the overlay; the array shape is used
// verbatim.
func ParseProviders(data []byte) ([]Provider, error) {
	trimmed := strings.TrimLeft(string(data), " \t\r\n")
	if strings.HasPrefix(trimmed, "[") {
		var providers []Provider
		if err := json.Unmarshal(data, &providers); err != nil {
			return nil, fmt.Errorf("failed to decode provider list: %w", err)
		}
		if len(providers) == 0 {
			return nil, fmt.Errorf("no providers found in the provided source")
		}
		return providers, nil
	}

	var md modelsDev
	if err := json.Unmarshal(data, &md); err != nil {
		return nil, fmt.Errorf("failed to decode provider data: %w", err)
	}
	if len(md) == 0 {
		return nil, fmt.Errorf("no providers found in the provided source")
	}
	return translateModelsDev(md), nil
}

// sortModels orders models newest-first using the release date so the
// default-model heuristic and the model picker surface current models
// first. Seed entries carry no release dates and keep their embedded
// order.
func sortModels(models []Model) {
	slices.SortStableFunc(models, func(a, b Model) int {
		if a.ContextWindow != b.ContextWindow {
			if a.ContextWindow > b.ContextWindow {
				return -1
			}
			return 1
		}
		return 0
	})
}
