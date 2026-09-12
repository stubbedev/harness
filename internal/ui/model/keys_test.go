package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDefaultKeyMap_BackgroundTasksBinding verifies the strip binding:
// ctrl+b must not collide with readline keys in the editor.
func TestDefaultKeyMap_BackgroundTasksBinding(t *testing.T) {
	t.Parallel()

	km := DefaultKeyMap()

	require.True(t, km.Chat.BackgroundTasks.Enabled(), "BackgroundTasks binding should be enabled")
	require.Contains(t, km.Chat.BackgroundTasks.Keys(), "ctrl+b")
}
