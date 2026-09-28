package history

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/db"
	"github.com/stubbedev/harness/internal/session"
)

// TestVersionsAreComputedByTheInsert pins the version sequence the
// history keeps per path: the first write of a session is the initial
// version, every later one the path's next, even when an initial write
// lands on a version the session already holds.
func TestVersionsAreComputedByTheInsert(t *testing.T) {
	t.Parallel()

	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	q := db.New(conn)
	sess, err := session.NewService(q, conn).Create(t.Context(), "history")
	require.NoError(t, err)
	files := NewService(q)

	first, err := files.Create(t.Context(), sess.ID, "/a.go", "v0")
	require.NoError(t, err)
	require.EqualValues(t, InitialVersion, first.Version)

	next, err := files.CreateVersion(t.Context(), sess.ID, "/a.go", "v1")
	require.NoError(t, err)
	require.EqualValues(t, 1, next.Version)

	again, err := files.Create(t.Context(), sess.ID, "/a.go", "v2")
	require.NoError(t, err, "a second initial write steps to the next version")
	require.EqualValues(t, 2, again.Version)

	other, err := files.CreateVersion(t.Context(), sess.ID, "/b.go", "b0")
	require.NoError(t, err)
	require.EqualValues(t, InitialVersion, other.Version, "a path with no history starts at the initial version")
}
