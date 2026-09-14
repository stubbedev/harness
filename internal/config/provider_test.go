package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/db"
)

// mockCatalogClient is a catalogClient stub for tests.
type mockCatalogClient struct {
	providers []catalog.Provider
	err       error
	calls     int
}

func (m *mockCatalogClient) FetchCatalog(context.Context) ([]catalog.Provider, error) {
	m.calls++
	return m.providers, m.err
}

func resetProviderState() {
	providerOnce = sync.Once{}
	providerList = nil
	providerErr = nil
	catalogSyncer = &catalogSync{}
	// Close pooled catalog-cache connections so temp data dirs can be
	// removed on Windows, where open files cannot be unlinked.
	db.ResetPool()
}

// seedCatalogDB writes a catalog row directly into the test database.
func seedCatalogDB(t *testing.T, dataDir string, providers []catalog.Provider, fetchedAt time.Time) {
	t.Helper()
	conn, err := db.Connect(context.Background(), dataDir)
	require.NoError(t, err)
	data, err := json.Marshal(providers)
	require.NoError(t, err)
	_, err = db.New(conn).SaveModelCatalog(context.Background(), string(data))
	require.NoError(t, err)
	// Backdate the row after the insert when needed.
	if !fetchedAt.IsZero() {
		_, err = conn.ExecContext(context.Background(), "UPDATE model_catalog SET fetched_at = ? WHERE id = 1", fetchedAt.Unix())
		require.NoError(t, err)
	}
}

func TestProviders_AutoUpdateDisabled(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	resetProviderState()
	defer resetProviderState()

	// With auto-update disabled the cached catalog is served at any
	// age and never fetched. The cache is machine-wide, not per
	// workspace, so it is seeded where GlobalCatalogDir points.
	cached := []catalog.Provider{
		{Name: "Cached", ID: "c1", Models: []catalog.Model{{ID: "m1"}}},
	}
	seedCatalogDB(t, GlobalCatalogDir(), cached, time.Now().Add(-72*time.Hour))

	cfg := &Config{
		Options: &Options{
			DisableProviderAutoUpdate: true,
			DataDirectory:             filepath.Join(tmpDir, "workspace"),
		},
	}

	providers, err := Providers(cfg)
	require.NoError(t, err)
	require.Len(t, providers, 1, "expected the cached catalog regardless of age")
	require.Equal(t, "Cached", providers[0].Name)
}

func TestProviders_HonorsDisableDefaultProviders(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	resetProviderState()
	defer resetProviderState()

	providers, err := Providers(&Config{
		Options: &Options{DisableDefaultProviders: true},
	})
	require.NoError(t, err)
	require.Empty(t, providers)
}

func TestCatalogSync_FreshDBCacheSkipsFetch(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(db.ResetPool)

	cached := []catalog.Provider{
		{Name: "Cached", ID: "c1", Models: []catalog.Model{{ID: "m1"}}},
	}
	seedCatalogDB(t, dataDir, cached, time.Now())

	client := &mockCatalogClient{providers: []catalog.Provider{
		{Name: "Fresh", ID: "fresh"},
	}}
	syncer := &catalogSync{}
	syncer.Init(client, dataDir, true)

	providers, err := syncer.Get(t.Context())
	require.NoError(t, err)
	require.Len(t, providers, 1)
	require.Equal(t, "Cached", providers[0].Name)
	require.Zero(t, client.calls, "a fresh cache row must not trigger a fetch")
}

func TestCatalogSync_StaleDBCacheUsedWhenFetchFails(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(db.ResetPool)

	cached := []catalog.Provider{
		{Name: "Stale", ID: "c1", Models: []catalog.Model{{ID: "m1"}}},
	}
	seedCatalogDB(t, dataDir, cached, time.Now().Add(-48*time.Hour))

	client := &mockCatalogClient{err: errors.New("network error")}
	syncer := &catalogSync{}
	syncer.Init(client, dataDir, true)

	providers, err := syncer.Get(t.Context())
	require.NoError(t, err, "a stale cache is a sound answer, not an error")
	require.Len(t, providers, 1)
	require.Equal(t, "Stale", providers[0].Name)
}

func TestCatalogSync_FetchSuccessStoresInDB(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(db.ResetPool)

	fresh := []catalog.Provider{
		{Name: "Fresh", ID: "f1", Models: []catalog.Model{{ID: "m1"}}},
	}
	client := &mockCatalogClient{providers: fresh}
	syncer := &catalogSync{}
	syncer.Init(client, dataDir, true)

	providers, err := syncer.Get(t.Context())
	require.NoError(t, err)
	require.Len(t, providers, 1)
	require.Equal(t, "Fresh", providers[0].Name)

	// The fetched catalog is persisted: a new syncer with a failing
	// client still sees it while the row is fresh.
	failing := &catalogSync{}
	failing.Init(&mockCatalogClient{err: errors.New("offline")}, dataDir, true)
	cached, err := failing.Get(t.Context())
	require.NoError(t, err)
	require.Len(t, cached, 1)
	require.Equal(t, "Fresh", cached[0].Name)
}

func TestCatalogSync_SeedUsedWhenFetchFailsWithNoCache(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(db.ResetPool)

	client := &mockCatalogClient{err: errors.New("network error")}
	syncer := &catalogSync{}
	syncer.Init(client, dataDir, true)

	providers, err := syncer.Get(t.Context())
	require.Error(t, err, "with no cache and no fetch the failure is still reported")
	require.NotEmpty(t, providers, "the snapshot bundled with the build carries the first run")

	_, ok := findProviderByID(providers, catalog.InferenceProviderAnthropic)
	require.True(t, ok, "the bundled snapshot is a real catalog")
}

func findProviderByID(providers []catalog.Provider, id catalog.InferenceProvider) (catalog.Provider, bool) {
	for _, p := range providers {
		if p.ID == id {
			return p, true
		}
	}
	return catalog.Provider{}, false
}

// TestProviders_KeepsCatalogWhenDBUnavailable covers a data directory
// that cannot host the cache database: the fetched catalog is still
// returned for this run even though it could not be persisted.
func TestProviders_KeepsCatalogWhenDBUnavailable(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "data"))
	blocked := tmpDir + "/blocked"
	require.NoError(t, os.WriteFile(blocked, []byte("block"), 0o644))

	resetProviderState()
	defer resetProviderState()

	cfg := &Config{Options: &Options{DataDirectory: blocked + "/data"}}
	providers, err := Providers(cfg)
	require.NoError(t, err)
	require.NotEmpty(t, providers, "a broken cache does not cost the user the catalog")
}

func TestProviders_UsesMockClientInjectedThroughSyncer(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(db.ResetPool)

	client := &mockCatalogClient{providers: []catalog.Provider{
		{Name: "Provider1", ID: "p1"},
		{Name: "Provider2", ID: "p2"},
	}}
	syncer := &catalogSync{}
	syncer.Init(client, dataDir, true)

	providers, err := syncer.Get(t.Context())
	require.NoError(t, err)
	require.Len(t, providers, 2)
}

// TestUpdateProviders_WritesTheStoreProvidersReads pins the contract
// the CLI depends on: `harness update-providers` must refresh the same
// catalog a session reads, not a store of its own.
func TestUpdateProviders_WritesTheStoreProvidersReads(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)
	t.Cleanup(db.ResetPool)

	resetProviderState()
	defer resetProviderState()

	source := filepath.Join(tmpDir, "providers.json")
	data, err := json.Marshal([]catalog.Provider{{
		Name:        "Local",
		ID:          "local",
		Type:        catalog.TypeOpenAICompat,
		APIEndpoint: "https://local.test/v1",
		Models:      []catalog.Model{{ID: "m1"}},
	}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(source, data, 0o600))

	require.NoError(t, UpdateProviders(source))

	// A session reads the catalog through Providers, from a workspace
	// data directory that has nothing to do with where the refresh ran.
	cfg := &Config{
		Options: &Options{
			DisableProviderAutoUpdate: true,
			DataDirectory:             filepath.Join(tmpDir, "workspaces", "somewhere"),
		},
	}
	providers, err := Providers(cfg)
	require.NoError(t, err)
	require.Len(t, providers, 1)
	require.Equal(t, "Local", providers[0].Name)
}

func TestUpdateProviderInList(t *testing.T) {
	resetProviderState()
	defer resetProviderState()

	providerList = []catalog.Provider{
		{ID: "a", Name: "A"},
		{ID: "b", Name: "B"},
	}

	UpdateProviderInList(catalog.Provider{ID: "b", Name: "B2"})
	require.Equal(t, "B2", providerList[1].Name)

	UpdateProviderInList(catalog.Provider{ID: "c", Name: "C"})
	require.Len(t, providerList, 3)
	require.Equal(t, "C", providerList[0].Name, "new providers are prepended")
}
