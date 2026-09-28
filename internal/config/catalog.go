package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/crash"
	"github.com/stubbedev/harness/internal/db"
)

// catalogRefreshInterval is how often the catalog is refreshed from the
// live sources. A row younger than this is served from the SQLite cache
// without a network round trip.
const catalogRefreshInterval = 24 * time.Hour

// catalogFetchTimeout bounds one live fetch, wherever it runs: the
// synchronous load that carries a first run with no cache, and the
// background refresh that keeps a stale catalog from blocking startup.
const catalogFetchTimeout = 45 * time.Second

// catalogClient fetches a fresh provider catalog from the live sources.
type catalogClient interface {
	FetchCatalog(ctx context.Context) ([]catalog.Provider, error)
}

// liveCatalogClient fetches from models.dev and OpenRouter.
type liveCatalogClient struct{}

func (liveCatalogClient) FetchCatalog(ctx context.Context) ([]catalog.Provider, error) {
	return catalog.FetchCatalog(ctx, nil)
}

var _ catalogClient = liveCatalogClient{}

// catalogSync memoizes the provider catalog for the process. The cache
// lives in a SQLite database shared by every workspace on the machine;
// when it has no row (first run) and the live fetch fails, the snapshot
// bundled in the binary is used, so harness always starts with a
// catalog.
//
// A row past its refresh interval is a different case: it is still a
// sound catalog, so it is served at once and the refresh runs in the
// background. Blocking the TUI on models.dev for up to the fetch
// timeout on the first launch of the day buys nothing the background
// does not - and the swap below publishes the fresh catalog to every
// later Get, which the coordinator reads per turn.
type catalogSync struct {
	once       sync.Once
	refreshing sync.Once
	mu         sync.RWMutex
	result     []catalog.Provider
	err        error
	client     catalogClient
	dataDir    string
	autoupdate bool
	// stale records that the served catalog is the cached one past its
	// refresh interval, so a later caller that allows auto-update can
	// still start the refresh a first caller that forbade it did not.
	stale bool
}

// Init configures the syncer; it must be called before Get.
func (s *catalogSync) Init(client catalogClient, dataDir string, autoupdate bool) {
	s.client = client
	s.dataDir = dataDir
	s.autoupdate = autoupdate
}

// Get returns the catalog under the refresh policy given to Init.
func (s *catalogSync) Get(ctx context.Context) ([]catalog.Provider, error) {
	return s.GetWith(ctx, s.autoupdate)
}

// GetWith returns the catalog. The load happens once per syncer, but the
// refresh policy is the caller's: a stale catalog served to a caller that
// disabled auto-update is still refreshed for the next one that allows it.
func (s *catalogSync) GetWith(ctx context.Context, autoupdate bool) ([]catalog.Provider, error) {
	// The result and the error are memoized together so that every
	// caller sees the same outcome, not just the one that won the once.
	s.once.Do(func() { s.load(ctx, autoupdate) })
	s.mu.RLock()
	defer s.mu.RUnlock()
	if autoupdate && s.stale {
		s.refreshInBackground()
	}
	return s.result, s.err
}

func (s *catalogSync) load(ctx context.Context, autoupdate bool) {
	conn, connErr := db.Connect(context.WithoutCancel(ctx), s.dataDir)
	if connErr != nil {
		slog.Warn("Could not open catalog cache database", "error", connErr)
	}
	// The connection is only used inside this once; release the
	// pooled reference so the handle does not outlive the caller
	// (tests remove the data directory, and Windows cannot unlink
	// open files).
	defer func() {
		if conn != nil {
			_ = db.Release(s.dataDir)
		}
	}()

	// Serve the cached catalog when it is fresh enough. This is the
	// common startup path: one small query, no network. With
	// auto-update disabled the cache is served at any age.
	if conn != nil {
		if row, getErr := db.New(conn).GetModelCatalog(ctx); getErr == nil {
			if providers, ok, current := usableCachedProviders(row.Data); ok {
				stale := !current || time.Since(time.Unix(row.FetchedAt, 0)) >= catalogRefreshInterval
				if !stale || !autoupdate {
					slog.Info("Using cached catalog", "fetched_at", time.Unix(row.FetchedAt, 0))
					s.serve(providers, nil)
					s.stale = stale
					return
				}
				// Stale but sound: hand it out now and let the refresh
				// land behind the caller's back.
				slog.Info("Using stale catalog; refreshing in the background", "fetched_at", time.Unix(row.FetchedAt, 0))
				s.serve(providers, nil)
				s.stale = true
				return
			}
		}
	}

	slog.Info("Fetching catalog from models.dev")
	result, fetchErr := s.fetch(ctx)
	if fetchErr == nil && len(result) > 0 {
		if conn != nil {
			if err := storeCatalog(ctx, conn, result); err != nil {
				s.serve(result, err)
				return
			}
		}
		s.serve(result, nil)
		return
	}

	// The fetch failed or came back empty. A stale database row is
	// the next-best answer. Being offline is routine, so this is
	// logged rather than reported to the caller unless nothing
	// usable exists at all.
	if conn != nil {
		if row, getErr := db.New(conn).GetModelCatalog(ctx); getErr == nil {
			if providers, ok, _ := usableCachedProviders(row.Data); ok {
				slog.Warn("Continuing with stale catalog", "fetched_at", time.Unix(row.FetchedAt, 0), "error", fetchErr)
				s.serve(providers, nil)
				return
			}
		}
	}
	if fetchErr == nil {
		fetchErr = errors.New("catalog sources returned no providers")
	}

	// Nothing live and nothing cached: fall back to the snapshot
	// bundled at build time. It ages, but a first run offline with
	// no providers at all cannot even reach the model picker.
	if seed := catalog.Seed(); len(seed) > 0 {
		slog.Warn("Could not fetch catalog; using the catalog bundled with this build", "error", fetchErr)
		s.serve(seed, fetchErr)
		return
	}

	slog.Warn("Could not fetch catalog; only manually configured providers are available", "error", fetchErr)
	s.serve(nil, fetchErr)
}

// serve records the outcome of the load for every caller of Get.
func (s *catalogSync) serve(providers []catalog.Provider, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.result, s.err = providers, err
}

// refreshInBackground fetches a fresh catalog off the caller's path and
// swaps it in once it lands: later Gets serve it, and the store update
// saves every other harness on the machine the fetch. It runs once per
// syncer - a refresh that failed is retried on the next launch, not in
// a loop.
func (s *catalogSync) refreshInBackground() {
	s.refreshing.Do(func() {
		crash.Go("catalog.refresh", func() {
			ctx, cancel := context.WithTimeout(context.Background(), catalogFetchTimeout)
			defer cancel()
			result, err := s.fetch(ctx)
			if err != nil || len(result) == 0 {
				slog.Warn("Background catalog refresh failed; keeping the stale catalog", "error", err)
				return
			}
			if conn, connErr := db.Connect(ctx, s.dataDir); connErr == nil {
				defer func() { _ = db.Release(s.dataDir) }()
				if err := storeCatalog(ctx, conn, result); err != nil {
					return
				}
			}
			slog.Info("Catalog refreshed in the background", "providers", len(result))
			s.serve(result, nil)
			s.mu.Lock()
			s.stale = false
			s.mu.Unlock()
		})
	})
}

// fetch performs one live fetch under the shared timeout.
func (s *catalogSync) fetch(ctx context.Context) ([]catalog.Provider, error) {
	ctx, cancel := context.WithTimeout(ctx, catalogFetchTimeout)
	defer cancel()
	return s.client.FetchCatalog(ctx)
}

// storeCatalog persists the catalog to the database. A failure only
// costs the next run a refresh, so it is logged and returned as an
// advisory error alongside a valid result.
// storedCatalog is a cached catalog row: the translated providers and the
// translation that produced them. Rows written before the version was
// recorded hold a bare provider list.
type storedCatalog struct {
	Translation string             `json:"translation"`
	Providers   []catalog.Provider `json:"providers"`
}

func storeCatalog(ctx context.Context, conn *sql.DB, providers []catalog.Provider) error {
	data, err := json.Marshal(storedCatalog{Translation: catalog.TranslationVersion(), Providers: providers})
	if err != nil {
		return fmt.Errorf("failed to marshal catalog: %w", err)
	}
	if _, err := db.New(conn).SaveModelCatalog(ctx, string(data)); err != nil {
		slog.Warn("Failed to save catalog to database", "error", err)
		return fmt.Errorf("failed to save catalog: %w", err)
	}
	return nil
}

// decodeCatalog decodes a stored catalog row and reports whether this
// build's translation produced it.
func decodeCatalog(data string) (providers []catalog.Provider, current bool, err error) {
	var stored storedCatalog
	if json.Unmarshal([]byte(data), &stored) == nil && stored.Providers != nil {
		return stored.Providers, stored.Translation == catalog.TranslationVersion(), nil
	}
	if err := json.Unmarshal([]byte(data), &providers); err != nil {
		return nil, false, fmt.Errorf("failed to decode cached catalog: %w", err)
	}
	return providers, false, nil
}

// usableCachedProviders decodes a stored catalog row and reports
// whether it carries a catalog worth serving. Usable is the one notion
// the fresh-serve, the stale-serve and the offline fallback must agree
// on, so it is defined once here: a row that decodes and is not empty.
// An empty list from a corrupt or half-written row must fall through to
// the live fetch, not masquerade as a working cache.
//
// current reports whether this build's translation produced the row; one
// that another build translated is usable but stale.
func usableCachedProviders(data string) (providers []catalog.Provider, usable, current bool) {
	providers, current, err := decodeCatalog(data)
	return providers, err == nil && len(providers) > 0, current
}
