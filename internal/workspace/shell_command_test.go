package workspace

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/app"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/db"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/session"
)

// TestRunShellCommand_SkipsPersistenceForMissingSession verifies a shell
// command for a session that does not exist still runs, and leaves no
// orphaned message behind.
func TestRunShellCommand_SkipsPersistenceForMissingSession(t *testing.T) {
	t.Parallel()

	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	q := db.New(conn)
	messages := message.NewService(q)
	w := NewAppWorkspace(
		&app.App{Sessions: session.NewService(q, conn), Messages: messages},
		config.NewTestStoreWithWorkingDir(&config.Config{}, t.TempDir()),
		WithEnv([]string{"HARNESS_TEST_GREETING=hello"}),
	)

	missingSessionID := uuid.New().String()
	resp, err := w.AgentRunShellCommand(t.Context(), missingSessionID, "echo $HARNESS_TEST_GREETING", 0, nil, false)
	require.NoError(t, err)
	require.Equal(t, "hello\n", resp.Output, "the registered environment reaches the command")
	require.Zero(t, resp.ExitCode)

	stored, err := messages.List(t.Context(), missingSessionID)
	require.NoError(t, err)
	require.Empty(t, stored)
}
