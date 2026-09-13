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
// sources return an empty list, the syncer falls back to the embedded
// seed and reports the failure.
func TestCatalogSync_GetEmptyResultFromClient(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	syncer := &catalogSync{}
	syncer.Init(&emptyProviderClient{}, dataDir, true)

	providers, err := syncer.Get(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "no providers")
	require.Empty(t, providers, "there is no built-in fallback catalog")

	// No catalog row is stored for empty results.
	conn, connErr := db.Connect(context.Background(), dataDir)
	require.NoError(t, connErr)
	_, getErr := db.New(conn).GetModelCatalog(t.Context())
	require.Error(t, getErr, "empty results are not persisted")
}
