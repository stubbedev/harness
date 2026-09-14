package keys

import (
	"maps"
	"reflect"
	"slices"

	"testing"

	"charm.land/bubbles/v2/key"

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

// bindingFields walks the keymap and returns every key.Binding field it
// holds, by address, named for the path that reaches it.
func bindingFields(t *testing.T, km *KeyMap) map[uintptr]string {
	t.Helper()

	found := make(map[uintptr]string)
	var walk func(v reflect.Value, path string)
	walk = func(v reflect.Value, path string) {
		typ := v.Type()
		for i := range typ.NumField() {
			field := v.Field(i)
			name := typ.Field(i).Name
			full := name
			if path != "" {
				full = path + "." + name
			}
			switch {
			case field.Type() == reflect.TypeOf(key.Binding{}):
				found[field.Addr().Pointer()] = full
			case field.Kind() == reflect.Struct:
				walk(field, full)
			}
		}
	}
	walk(reflect.ValueOf(km).Elem(), "")
	return found
}

// TestEveryBindingHasAnActionName pins the chokepoint: a binding added to
// the keymap must also be named in keybindActions, or it silently becomes
// the one key in the TUI a user cannot rebind.
func TestEveryBindingHasAnActionName(t *testing.T) {
	t.Parallel()

	km := DefaultKeyMap()
	fields := bindingFields(t, &km)

	named := make(map[uintptr]string, len(fields))
	for action, binding := range km.keybindActions() {
		addr := reflect.ValueOf(binding).Pointer()
		if other, dup := named[addr]; dup {
			t.Errorf("actions %q and %q both rebind the same binding", other, action)
		}
		named[addr] = action
	}

	for addr, path := range fields {
		if _, ok := named[addr]; !ok {
			t.Errorf("KeyMap.%s has no name in keybindActions, so options.tui.keybinds cannot rebind it", path)
		}
	}
	require.Len(t, named, len(fields), "every action name must point at a binding in the keymap")
}

// TestActionNamesMatchTheRegistry pins that the names published to the
// config schema are exactly the ones overrides are applied against.
func TestActionNamesMatchTheRegistry(t *testing.T) {
	t.Parallel()

	km := DefaultKeyMap()
	require.Equal(t, slices.Sorted(maps.Keys(km.keybindActions())), ActionNames())
}

// TestInstallIsWhatActiveServes pins the second chokepoint: components
// that cannot reach the config read their keys from Active, so Install has
// to be what they see.
func TestInstallIsWhatActiveServes(t *testing.T) {
	// Not parallel: this installs the process keymap.
	t.Cleanup(func() { active.Store(nil) })

	require.Equal(t, DefaultKeyMap().Dialog.Select.Keys(), Active().Dialog.Select.Keys())

	Install(map[string][]string{"dialog.select": {"ctrl+space"}})
	require.Equal(t, []string{"ctrl+space"}, Active().Dialog.Select.Keys())
}
