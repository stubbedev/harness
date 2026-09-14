package config

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/db"
)

type syncer[T any] interface {
	Get(context.Context) (T, error)
}

var (
	providerOnce sync.Once
	providerList []catalog.Provider
	providerErr  error
)

var catalogSyncer = &catalogSync{}

// Providers returns the list of providers, taking into account the
// shared catalog cache and whether or not auto update is enabled.
//
// It will:
// 1. load the cached catalog from the shared SQLite database when it is
// less than a day old (at any age when auto update is disabled).
// 2. otherwise try to get the fresh list from models.dev (plus
// OpenRouter for the openrouter entry), and return either this new
// list, the stale cached list, or finally the snapshot bundled with the
// build if the fetch fails.
//
// A returned error is advisory: it reports that the catalog could not
// be refreshed or cached. Callers decide whether an empty catalog is
// fatal.
func Providers(cfg *Config) ([]catalog.Provider, error) {
	providerOnce.Do(func() {
		autoupdate := !cfg.Options.DisableProviderAutoUpdate
		customProvidersOnly := cfg.Options.DisableDefaultProviders

		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()

		if customProvidersOnly {
			providerList = nil
			return
		}

		client := liveCatalogClient{}
		catalogSyncer.Init(client, GlobalCatalogDir(), autoupdate)

		// A failure to refresh or cache the catalog is worth reporting
		// to the caller, which decides whether an empty catalog is
		// fatal or the manually configured providers suffice.
		items, err := catalogSyncer.Get(ctx)
		if err != nil {
			err = fmt.Errorf( //nolint:staticcheck
				"Harness was unable to fetch an updated model catalog. You can also update providers manually. For more info see harness update-providers --help.\n\nCause: %w",
				err,
			)
		}
		providerList = items
		providerErr = err
	})
	return providerList, providerErr
}

// UpdateProviderInList replaces a provider in the memoized provider list
// returned by Providers(). This is used after re-fetching a single
// provider's data so that all callers of Providers() see the updated
// entry without needing to reset sync.Once.
func UpdateProviderInList(provider catalog.Provider) {
	for i, p := range providerList {
		if p.ID == provider.ID {
			providerList[i] = provider
			return
		}
	}
	// Provider not found in list; prepend it.
	providerList = append([]catalog.Provider{provider}, providerList...)
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

	// Make the fresh catalog visible to the running process so the
	// model picker sees it without a restart.
	for _, p := range providers {
		UpdateProviderInList(p)
	}

	slog.Info("Providers updated successfully", "count", len(providers), "from", cmpOrString(pathOrURL, "models.dev"))
	return nil
}

// fetchCatalogFromHTTP downloads pathOrURL and decodes it with the same
// rules as local files.
func fetchCatalogFromHTTP(ctx context.Context, url string) ([]catalog.Provider, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("could not create request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch %s: %w", url, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d from %s", resp.StatusCode, url)
	}
	content, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read response from %s: %w", url, err)
	}
	providers, err := catalog.ParseProviders(content)
	if err != nil {
		return nil, err
	}
	return providers, nil
}

func cmpOrString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
