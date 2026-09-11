package styles

import (
	"fmt"
	"image/color"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadTheme(t *testing.T) {
	t.Parallel()

	t.Run("empty name yields default", func(t *testing.T) {
		t.Parallel()
		s, err := LoadTheme("")
		require.NoError(t, err)
		require.Equal(t, CharmtonePantera(), s)
	})

	t.Run("case-insensitive", func(t *testing.T) {
		t.Parallel()
		lower, err := LoadTheme("catppuccin-mocha")
		require.NoError(t, err)
		upper, err := LoadTheme("Catppuccin-Mocha")
		require.NoError(t, err)
		require.Equal(t, lower, upper)
	})

	t.Run("unknown name errors and lists themes", func(t *testing.T) {
		t.Parallel()
		_, err := LoadTheme("nope")
		require.ErrorContains(t, err, "unknown theme")
		require.ErrorContains(t, err, "charmtone")
		require.ErrorContains(t, err, "catppuccin-mocha")
		require.ErrorContains(t, err, "gruvbox-dark")
	})

	t.Run("every builtin loads", func(t *testing.T) {
		t.Parallel()
		for _, name := range BuiltinThemeNames() {
			s, err := LoadTheme(name)
			require.NoError(t, err, "builtin %s must load", name)
			require.NotNil(t, s.Header.Percentage, "builtin %s must produce styles", name)
		}
	})

	t.Run("catppuccin-mocha uses the palette", func(t *testing.T) {
		t.Parallel()
		s, err := LoadTheme("catppuccin-mocha")
		require.NoError(t, err)
		// mauve (#cba6f7) drives the working gradient.
		require.Equal(t, "#cba6f7", colorHex(s.WorkingGradFromColor))
		// base (#1e1e2e) is the background.
		require.Equal(t, "#1e1e2e", colorHex(s.Background))
	})

	t.Run("themes are distinct", func(t *testing.T) {
		t.Parallel()
		charmtone := ThemeFromConfig("charmtone")
		mocha := ThemeFromConfig("catppuccin-mocha")
		gruvbox := ThemeFromConfig("gruvbox-dark")
		require.NotEqual(t, colorHex(charmtone.WorkingGradFromColor), colorHex(mocha.WorkingGradFromColor))
		require.NotEqual(t, colorHex(mocha.WorkingGradFromColor), colorHex(gruvbox.WorkingGradFromColor))
	})
}

func TestThemeFromConfig_FallsBackOnUnknown(t *testing.T) {
	t.Parallel()
	s := ThemeFromConfig("not-a-theme")
	require.Equal(t, CharmtonePantera(), s)
}

func colorHex(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
}
