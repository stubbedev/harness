package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/db"
)

// catalogRefreshInterval is how often the catalog is refreshed from the
// live sources. A row younger than this is served from the SQLite cache
// without a network round trip.
const catalogRefreshInterval = 24 * time.Hour

// catalogClient fetches a fresh provider catalog from the live sources.
type catalogClient interface {
	FetchCatalog(ctx context.Context) ([]catalog.Provider, error)
}

// liveCatalogClient fetches from models.dev and OpenRouter.
type liveCatalogClient struct{}

func (liveCatalogClient) FetchCatalog(ctx context.Context) ([]catalog.Provider, error) {
	return catalog.FetchCatalog(ctx, nil)
}

var (
	_ syncer[[]catalog.Provider] = (*catalogSync)(nil)
	_ catalogClient              = liveCatalogClient{}
)

// catalogSync memoizes the provider catalog for the process. The cache
// lives in the harness SQLite database; the embedded seed catalog is
// the last-resort fallback when the database has no row (first run) and
// the live fetch fails.
type catalogSync struct {
	once       sync.Once
	result     []catalog.Provider
	err        error
	client     catalogClient
	dataDir    string
	autoupdate bool
	init       atomic.Bool
}

func (s *catalogSync) Init(client catalogClient, dataDir string, autoupdate bool) {
	s.client = client
	s.dataDir = dataDir
	s.autoupdate = autoupdate
	s.init.Store(true)
}

func (s *catalogSync) Get(ctx context.Context) ([]catalog.Provider, error) {
	if !s.init.Load() {
		panic("called Get before Init")
	}

	// The result and the error are memoized together so that every
	// caller sees the same outcome, not just the one that won the once.
	s.once.Do(func() {
		if !s.autoupdate {
			slog.Info("Using embedded seed catalog (auto-update disabled)")
			s.result = catalog.Embedded()
			return
		}

		conn, connErr := db.Connect(context.WithoutCancel(ctx), s.dataDir)
		if connErr != nil {
			slog.Warn("Could not open catalog cache database", "error", connErr)
		}

		// Serve the cached catalog when it is fresh enough. This is the
		// common startup path: one small query, no network.
		if conn != nil {
			if row, getErr := db.New(conn).GetModelCatalog(ctx); getErr == nil {
				providers, decodeErr := decodeCatalog(row.Data)
				if decodeErr == nil && len(providers) > 0 &&
					time.Since(time.Unix(row.FetchedAt, 0)) < catalogRefreshInterval {
					slog.Info("Using cached catalog", "fetched_at", time.Unix(row.FetchedAt, 0))
					s.result = providers
					return
				}
			}
		}

		slog.Info("Fetching catalog from models.dev")
		result, fetchErr := s.client.FetchCatalog(ctx)
		if fetchErr == nil && len(result) > 0 {
			s.result = result
			if conn != nil {
				s.err = storeCatalog(ctx, conn, result)
			}
			return
		}

		// The fetch failed or came back empty. A stale database row is
		// the next-best answer, and the embedded seed is the last one.
		// Being offline is routine, so this is logged rather than
		// reported to the caller unless nothing usable exists at all.
		if conn != nil {
			if row, getErr := db.New(conn).GetModelCatalog(ctx); getErr == nil {
				if providers, decodeErr := decodeCatalog(row.Data); decodeErr == nil && len(providers) > 0 {
					slog.Warn("Continuing with stale catalog", "fetched_at", time.Unix(row.FetchedAt, 0), "error", fetchErr)
					s.result = providers
					return
				}
			}
		}
		if fetchErr == nil {
			fetchErr = errors.New("catalog sources returned no providers")
		}
		slog.Warn("Could not fetch catalog, using embedded seed", "error", fetchErr)
		s.result = catalog.Embedded()
		s.err = fetchErr
	})
	return s.result, s.err
}

// storeCatalog persists the catalog to the database. A failure only
// costs the next run a refresh, so it is logged and returned as an
// advisory error alongside a valid result.
func storeCatalog(ctx context.Context, conn *sql.DB, providers []catalog.Provider) error {
	data, err := json.Marshal(providers)
	if err != nil {
		return fmt.Errorf("failed to marshal catalog: %w", err)
	}
	if _, err := db.New(conn).SaveModelCatalog(ctx, string(data)); err != nil {
		slog.Warn("Failed to save catalog to database", "error", err)
		return fmt.Errorf("failed to save catalog: %w", err)
	}
	return nil
}

// decodeCatalog decodes a stored catalog row.
func decodeCatalog(data string) ([]catalog.Provider, error) {
	var providers []catalog.Provider
	if err := json.Unmarshal([]byte(data), &providers); err != nil {
		return nil, fmt.Errorf("failed to decode cached catalog: %w", err)
	}
	return providers, nil
}
