package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/client"
	"github.com/stubbedev/harness/internal/proto"
)

// TestResolveSessionLastSkipsChildSessions verifies --continue --last
// never resumes a subagent session, even when the newest or the first
// listed session is a child.
func TestResolveSessionLastSkipsChildSessions(t *testing.T) {
	t.Parallel()

	sessions := []proto.Session{
		{ID: "child", ParentSessionID: "parent", UpdatedAt: 300},
		{ID: "parent", UpdatedAt: 100},
		{ID: "newer", UpdatedAt: 200},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(sessions)
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	c, err := client.NewClient(t.TempDir(), "tcp", u.Host)
	require.NoError(t, err)

	got, err := resolveSession(t.Context(), c, "ws", "", true)
	require.NoError(t, err)
	require.Equal(t, "newer", got.ID)
}
