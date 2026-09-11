package model

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// TestThemePreviewRestoresFirstSnapshot verifies that walking through
// several themes still restores the one the picker opened on, not the
// previous entry in the list.
func TestThemePreviewRestoresFirstSnapshot(t *testing.T) {
	t.Parallel()

	original := styles.ThemeFromConfig("charmtone")
	var p themePreview

	p.begin(original, "config:charmtone")
	require.True(t, p.set("gruvbox-dark"))

	// A second and third move keep the original snapshot.
	p.begin(styles.ThemeFromConfig("gruvbox-dark"), "config:gruvbox-dark")
	require.True(t, p.set("catppuccin-mocha"))
	p.begin(styles.ThemeFromConfig("catppuccin-mocha"), "config:catppuccin-mocha")

	restore, key, ok := p.take()
	require.True(t, ok)
	require.Equal(t, "config:charmtone", key)
	require.Equal(t, original.Background, restore.Background)
}

// TestThemePreviewSkipsRepeats verifies that landing on the theme already
// previewed reports no change, so the UI skips the style rebuild.
func TestThemePreviewSkipsRepeats(t *testing.T) {
	t.Parallel()

	var p themePreview
	p.begin(styles.ThemeFromConfig("charmtone"), "config:charmtone")

	require.True(t, p.set("gruvbox-dark"), "first preview must apply")
	require.False(t, p.set("gruvbox-dark"), "same theme must not re-apply")
	require.True(t, p.set("catppuccin-mocha"), "a different theme must apply")
}

// TestThemePreviewTakeWithoutPreview verifies that closing the picker
// without previewing anything restores nothing.
func TestThemePreviewTakeWithoutPreview(t *testing.T) {
	t.Parallel()

	var p themePreview
	_, _, ok := p.take()
	require.False(t, ok)
}

// TestThemePreviewTakeIsOneShot verifies the state resets after a revert,
// so a later close can't revert a second time onto a committed theme.
func TestThemePreviewTakeIsOneShot(t *testing.T) {
	t.Parallel()

	var p themePreview
	p.begin(styles.ThemeFromConfig("charmtone"), "config:charmtone")
	p.set("gruvbox-dark")

	_, _, ok := p.take()
	require.True(t, ok)

	_, _, ok = p.take()
	require.False(t, ok, "the snapshot must be consumed")
}

// TestThemePreviewClearKeepsAppliedTheme verifies that committing a
// selection drops the snapshot, so closing the dialog afterwards does not
// undo the theme the user just picked.
func TestThemePreviewClearKeepsAppliedTheme(t *testing.T) {
	t.Parallel()

	var p themePreview
	p.begin(styles.ThemeFromConfig("charmtone"), "config:charmtone")
	p.set("gruvbox-dark")
	p.clear()

	_, _, ok := p.take()
	require.False(t, ok)

	// A later preview starts a fresh snapshot rather than reusing the
	// committed one.
	p.begin(styles.ThemeFromConfig("gruvbox-dark"), "config:gruvbox-dark")
	_, key, ok := p.take()
	require.True(t, ok)
	require.Equal(t, "config:gruvbox-dark", key)
}
