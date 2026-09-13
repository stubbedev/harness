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

func TestKeyMapApplyKeybinds(t *testing.T) {
	t.Parallel()

	t.Run("rebinds listed actions and merges over the defaults", func(t *testing.T) {
		t.Parallel()

		km := DefaultKeyMap()
		km.ApplyKeybinds(map[string][]string{
			"quit":           {"ctrl+q"},
			"editor.newline": {"shift+enter", "ctrl+j"},
		})

		require.Equal(t, []string{"ctrl+q"}, km.Quit.Keys())
		require.Equal(t, []string{"shift+enter", "ctrl+j"}, km.Editor.Newline.Keys())

		// Unlisted actions keep their defaults.
		def := DefaultKeyMap()
		require.Equal(t, def.Editor.SendMessage.Keys(), km.Editor.SendMessage.Keys())
		require.Equal(t, def.Chat.Copy.Keys(), km.Chat.Copy.Keys())
	})

	t.Run("keeps the help description and follows the new keys", func(t *testing.T) {
		t.Parallel()

		km := DefaultKeyMap()
		km.ApplyKeybinds(map[string][]string{
			"quit": {"ctrl+q", "ctrl+Q"},
		})

		help := km.Quit.Help()
		require.Equal(t, "quit", help.Desc)
		require.Equal(t, "ctrl+q/ctrl+Q", help.Key)
	})

	t.Run("leaves help-less bindings help-less", func(t *testing.T) {
		t.Parallel()

		km := DefaultKeyMap()
		km.ApplyKeybinds(map[string][]string{
			"chat.end_follow": {"alt+end"},
		})

		require.Equal(t, []string{"alt+end"}, km.Chat.EndFollow.Keys())
		help := km.Chat.EndFollow.Help()
		require.Empty(t, help.Desc)
		require.Empty(t, help.Key)
	})

	t.Run("warns and ignores unknown actions and empty key lists", func(t *testing.T) {
		t.Parallel()

		km := DefaultKeyMap()
		km.ApplyKeybinds(map[string][]string{
			"editor.does_not_exist": {"ctrl+q"},
			"quit":                  {},
		})

		require.Equal(t, DefaultKeyMap().Quit.Keys(), km.Quit.Keys())
	})

	t.Run("covers every action with a non-empty default", func(t *testing.T) {
		t.Parallel()

		// Every bindable action must point at a real binding, and a
		// rebind through the table must change it.
		km := DefaultKeyMap()
		for action, binding := range km.keybindActions() {
			require.NotEmpty(t, binding.Keys(), "action %s has an empty default binding", action)

			*binding = rebind(*binding, []string{"f13"})
			require.Equal(t, []string{"f13"}, binding.Keys(), "action %s did not rebind", action)
		}
	})
}
