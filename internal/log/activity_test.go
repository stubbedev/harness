package log

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProviderTransportReportsReadActivity(t *testing.T) {
	t.Parallel()

	const keepalives = 3
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for range keepalives {
			_, _ = io.WriteString(w, "event: ping\ndata: {\"type\": \"ping\"}\n\n")
			flusher.Flush()
		}
	}))
	t.Cleanup(server.Close)

	var reads atomic.Int64
	ctx := WithReadActivity(t.Context(), func() { reads.Add(1) })
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	require.NoError(t, err)

	resp, err := NewProviderHTTPClient(false, false).Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })
	require.Equal(t, int64(1), reads.Load(), "the response headers count as activity")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "event: ping")
	require.Greater(t, reads.Load(), int64(1), "keep-alive bytes on the wire must count as activity")
}

func TestProviderTransportLeavesUntrackedRequestsAlone(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(server.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	resp, err := NewProviderHTTPClient(false, false).Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })

	_, wrapped := resp.Body.(*activityBody)
	require.False(t, wrapped, "a request without a read-activity callback keeps its body as is")
}
