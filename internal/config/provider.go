package config

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/db"
)

// providerOnce points the process-wide syncer at the live sources and
// the machine-wide store. Nothing config-specific happens under it: each
// call to Providers applies its own config's options.
var providerOnce sync.Once

var catalogSyncer = &catalogSync{}

// KnownProviderByID returns the catalog provider with the given ID, or
// nil when it is not a built-in catalog provider.
func KnownProviderByID(knownProviders []catalog.Provider, id string) *catalog.Provider {
	for i, p := range knownProviders {
		if string(p.ID) == id {
			return &knownProviders[i]
		}
	}
	return nil
}

// Providers returns the list of providers, taking into account the
// shared catalog cache and whether or not auto update is enabled.
//
// The catalog is loaded once per process; later calls read the syncer's
// current answer. That is the point of reading through rather than
// snapshotting: a catalog that was stale at startup is refreshed in the
// background, and the fresh list is what every later call - the next
// turn's tool-palette rebuild included - sees. The options are applied
// per call, so one workspace disabling the default providers or
// auto-update does not decide it for every other workspace the process
// serves.
//
// A returned error is advisory: it reports that the catalog could not
// be refreshed or cached. Callers decide whether an empty catalog is
// fatal.
func Providers(cfg *Config) ([]catalog.Provider, error) {
	if cfg.Options.DisableDefaultProviders {
		return nil, nil
	}
	providerOnce.Do(func() {
		catalogSyncer.Init(liveCatalogClient{}, GlobalCatalogDir(), true)
	})
	// A failure to refresh or cache the catalog is worth reporting to
	// the caller, which decides whether an empty catalog is fatal or
	// the manually configured providers suffice.
	items, err := catalogSyncer.GetWith(context.Background(), !cfg.Options.DisableProviderAutoUpdate)
	if err != nil {
		err = fmt.Errorf( //nolint:staticcheck
			"Harness was unable to fetch an updated model catalog. You can also update providers manually. For more info see harness update-providers --help.\n\nCause: %w",
			err,
		)
	}
	return items, err
}

// UpdateProviders refreshes the stored model catalog. With no argument
// the catalog is fetched live from models.dev and OpenRouter. A path
// or URL is read as either a models.dev api.json document or a plain
// provider list. The catalog is written to the machine-wide store every
// workspace reads, so a refresh here is a refresh everywhere.
func UpdateProviders(pathOrURL string) error {
	var providers []catalog.Provider
	var err error

	switch {
	case pathOrURL == "":
		providers, err = catalog.FetchCatalog(context.Background(), nil)
		if err != nil {
			return fmt.Errorf("failed to fetch catalog: %w", err)
		}
	case strings.HasPrefix(pathOrURL, "http://") || strings.HasPrefix(pathOrURL, "https://"):
		providers, err = fetchCatalogFromHTTP(context.Background(), pathOrURL)
		if err != nil {
			return err
		}
	default:
		content, readErr := os.ReadFile(pathOrURL)
		if readErr != nil {
			return fmt.Errorf("failed to read file: %w", readErr)
		}
		providers, err = catalog.ParseProviders(content)
		if err != nil {
			return err
		}
	}

	catalogDir := GlobalCatalogDir()
	conn, err := db.Connect(context.Background(), catalogDir)
	if err != nil {
		return fmt.Errorf("failed to open catalog database: %w", err)
	}
	defer func() { _ = db.Release(catalogDir) }()
	if err := storeCatalog(context.Background(), conn, providers); err != nil {
		return err
	}

	slog.Info("Providers updated successfully", "count", len(providers), "from", cmp.Or(pathOrURL, "models.dev"))
	return nil
}

// fetchCatalogFromHTTP downloads pathOrURL and decodes it with the same
// rules as local files.
func fetchCatalogFromHTTP(ctx context.Context, url string) ([]catalog.Provider, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	content, err := catalog.FetchBytes(ctx, client, url)
	if err != nil {
		return nil, err
	}
	return catalog.ParseProviders(content)
}
