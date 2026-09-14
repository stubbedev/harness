package config

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/db"
)

type emptyProviderClient struct{}

func (m *emptyProviderClient) FetchCatalog(context.Context) ([]catalog.Provider, error) {
	return []catalog.Provider{}, nil
}

// TestCatalogSync_GetEmptyResultFromClient tests that when the live
// sources return an empty list, the syncer falls back to the snapshot
// bundled with the build and still reports the failure.
func TestCatalogSync_GetEmptyResultFromClient(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	t.Cleanup(db.ResetPool)

	syncer := &catalogSync{}
	syncer.Init(&emptyProviderClient{}, dataDir, true)

	providers, err := syncer.Get(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "no providers")
	require.Equal(t, catalog.Seed(), providers, "the bundled snapshot stands in")

	// No catalog row is stored for empty results: the seed is a
	// fallback, not something to cache back over the real catalog.
	conn, connErr := db.Connect(context.Background(), dataDir)
	require.NoError(t, connErr)
	_, getErr := db.New(conn).GetModelCatalog(t.Context())
	require.Error(t, getErr, "empty results are not persisted")
}
