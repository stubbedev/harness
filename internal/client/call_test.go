package client

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPathSegmentsAreEscaped verifies an ID holding path or query
// syntax stays one segment instead of reaching a different route.
func TestPathSegmentsAreEscaped(t *testing.T) {
	t.Parallel()

	var gotRaw, gotLSP string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/workspaces/{id}/lsps/{lsp}/diagnostics", func(w http.ResponseWriter, r *http.Request) {
		gotRaw = r.URL.EscapedPath()
		gotLSP = r.PathValue("lsp")
		_, _ = w.Write([]byte("{}"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := captureClient(t, srv).GetLSPDiagnostics(t.Context(), "ws1", "a/b?c")
	require.NoError(t, err)
	require.Equal(t, "a/b?c", gotLSP)
	require.Equal(t, "/v1/workspaces/ws1/lsps/a%2Fb%3Fc/diagnostics", gotRaw)
}
