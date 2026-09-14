package catalog

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
)

// seedData is a snapshot of the translated catalog, taken at release
// time and shipped in the binary. It is the last resort behind the
// database cache and the live sources: a first run with no network
// would otherwise have no providers at all, which is a worse failure
// than slightly stale model lists.
//
// Refresh it with `go generate ./internal/catalog`, which re-fetches
// the live sources and rewrites this file.
//
//go:generate go run ./seedgen -o seed.json.gz
//go:embed seed.json.gz
var seedData []byte

var (
	seedOnce      sync.Once
	seedProviders []Provider
)

// Seed returns the bundled catalog snapshot. The result is decoded once
// and shared, so callers must not mutate it.
func Seed() []Provider {
	seedOnce.Do(func() {
		gz, err := gzip.NewReader(bytes.NewReader(seedData))
		if err != nil {
			slog.Error("Could not read the bundled catalog seed", "error", err)
			return
		}
		defer gz.Close() //nolint:errcheck

		raw, err := io.ReadAll(gz)
		if err != nil {
			slog.Error("Could not decompress the bundled catalog seed", "error", err)
			return
		}
		if err := json.Unmarshal(raw, &seedProviders); err != nil {
			slog.Error("Could not decode the bundled catalog seed", "error", err)
			seedProviders = nil
		}
	})
	return seedProviders
}
