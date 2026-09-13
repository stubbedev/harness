package styles

import (
	"fmt"
	"image/color"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/charmtone"
)

// ThemeKeyForProvider returns a stable identifier for the theme
// associated with the given provider ID. Providers that share a theme
// yield the same key, so callers can cheaply detect when switching
// providers would not actually change the active theme and skip the
// expensive style rebuild. This is the single source of truth for the
// provider-to-theme mapping; [ThemeForProvider] builds on it.
func ThemeKeyForProvider(providerID string) string {
	return "default"
}

// ThemeForProvider returns the Styles associated with the given provider
// ID. Unknown or empty provider IDs yield the default Charmtone Pantera
// theme. A theme configured via options.tui.theme always takes
// precedence over this provider mapping; see [ThemeFromConfig].
func ThemeForProvider(providerID string) Styles {
	return CharmtonePantera()
}

// CharmtonePantera returns the Charmtone dark theme. It's the default style
// for the UI.
func CharmtonePantera() Styles {
	return charmtoneOverrides(quickStyle(charmtoneOpts()))
}

// charmtoneOpts returns the quickStyleOpts palette for the Charmtone dark
// theme, using colors from the upstream charmbracelet/x/exp/charmtone
// package.
func charmtoneOpts() quickStyleOpts {
	return quickStyleOpts{
		primary:   charmtone.Charple,
		secondary: charmtone.Dolly,
		accent:    charmtone.Bok,
		keyword:   charmtone.Blush,

		fgBase:       charmtone.Sash,
		fgMoreSubtle: charmtone.Squid,
		fgSubtle:     charmtone.Smoke,
		fgMostSubtle: charmtone.Oyster,

		onPrimary: charmtone.Butter,

		bgBase:         charmtone.Pepper,
		bgLeastVisible: charmtone.BBQ,
		bgLessVisible:  charmtone.Char,
		bgMostVisible:  charmtone.Iron,

		separator: charmtone.Char,

		destructive:       charmtone.Coral,
		error:             charmtone.Sriracha,
		warningSubtle:     charmtone.Zest,
		warning:           charmtone.Mustard,
		attention:         charmtone.Tang,
		busy:              charmtone.Citron,
		info:              charmtone.Malibu,
		infoMoreSubtle:    charmtone.Sardine,
		infoMostSubtle:    charmtone.Damson,
		success:           charmtone.Julep,
		successMoreSubtle: charmtone.Bok,
		successMostSubtle: charmtone.Guac,

		// ANSI 16-color palette for remapping raw terminal output
		// (e.g. bang-mode shell commands) onto legible Charmtone colors.
		ansiBlack:   charmtone.BBQ,
		ansiRed:     charmtone.Coral,
		ansiGreen:   charmtone.Guac,
		ansiYellow:  charmtone.Mustard,
		ansiBlue:    charmtone.Charple,
		ansiMagenta: charmtone.Dolly,
		ansiCyan:    charmtone.Malibu,
		ansiWhite:   charmtone.Smoke,

		ansiBrightBlack:   charmtone.Iron,
		ansiBrightRed:     charmtone.Tuna,
		ansiBrightGreen:   charmtone.Julep,
		ansiBrightYellow:  charmtone.Zest,
		ansiBrightBlue:    charmtone.Guppy,
		ansiBrightMagenta: charmtone.Blush,
		ansiBrightCyan:    charmtone.Sardine,
		ansiBrightWhite:   charmtone.Salt,

		// Subagent identity palette, in SubagentColorNames order:
		// red, orange, yellow, green, cyan, blue, purple, pink.
		subagentPalette: [8]color.Color{
			charmtone.Cherry,
			charmtone.Tang,
			charmtone.Citron,
			charmtone.Julep,
			charmtone.Guppy,
			charmtone.Sapphire,
			charmtone.Mauve,
			charmtone.Flamingo,
		},
	}
}

// charmtoneOverrides applies Charmtone-specific tweaks that don't fit the
// token model of [quickStyleOpts].
func charmtoneOverrides(s Styles) Styles {
	// Bang ! prompt overrides - use Salt/Hazy/Larple colors.
	s.Editor.PromptBangIconFocused = s.Editor.PromptBangIconFocused.
		Foreground(charmtone.Salt).
		Background(charmtone.Hazy)
	s.Editor.PromptBangDotsFocused = s.Editor.PromptBangDotsFocused.
		Foreground(charmtone.Hazy)
	s.Editor.PromptBangDotsBlurred = s.Editor.PromptBangDotsBlurred.
		Foreground(charmtone.Larple)

	// Shell bar/prompt overrides - use Charple/Iron/Hazy colors.
	s.Messages.ShellBarFocused = s.Messages.ShellBarFocused.
		BorderForeground(charmtone.Charple)
	s.Messages.ShellBarBlurred = s.Messages.ShellBarBlurred.
		BorderForeground(charmtone.Iron)
	s.Messages.ShellPrompt = s.Messages.ShellPrompt.
		Foreground(charmtone.Hazy)
	s.Messages.ShellPromptBlurred = s.Messages.ShellPromptBlurred.
		Foreground(charmtone.Hazy)

	return s
}

// catppuccinMochaOpts returns the quickStyleOpts palette for Catppuccin
// Mocha, mapped by role the same way the terminal palette in the user's
// dotfiles assigns it: mauve drives the UI accent (prompt, focused
// borders, links), pink the secondary accent, and crust is the dark
// foreground laid over those pastel backgrounds.
func catppuccinMochaOpts() quickStyleOpts {
	return quickStyleOpts{
		primary:   lipgloss.Color("#cba6f7"), // mauve
		secondary: lipgloss.Color("#f5c2e7"), // pink
		accent:    lipgloss.Color("#b4befe"), // lavender
		keyword:   lipgloss.Color("#cba6f7"), // mauve

		fgBase:       lipgloss.Color("#cdd6f4"), // text
		fgSubtle:     lipgloss.Color("#a6adc8"), // subtext0
		fgMoreSubtle: lipgloss.Color("#7f849c"), // overlay1
		fgMostSubtle: lipgloss.Color("#6c7086"), // overlay0

		onPrimary: lipgloss.Color("#11111b"), // crust

		bgBase:         lipgloss.Color("#1e1e2e"), // base
		bgLeastVisible: lipgloss.Color("#181825"), // mantle
		bgLessVisible:  lipgloss.Color("#313244"), // surface0
		bgMostVisible:  lipgloss.Color("#585b70"), // surface2

		separator: lipgloss.Color("#313244"), // surface0

		destructive:       lipgloss.Color("#eba0ac"), // maroon
		error:             lipgloss.Color("#f38ba8"), // red
		warningSubtle:     lipgloss.Color("#f9e2af"), // yellow
		warning:           lipgloss.Color("#fab387"), // peach
		attention:         lipgloss.Color("#fab387"), // peach
		busy:              lipgloss.Color("#f9e2af"), // yellow
		info:              lipgloss.Color("#89b4fa"), // blue
		infoMoreSubtle:    lipgloss.Color("#74c7ec"), // sapphire
		infoMostSubtle:    lipgloss.Color("#89dceb"), // sky
		success:           lipgloss.Color("#94e2d5"), // teal
		successMoreSubtle: lipgloss.Color("#94e2d5"), // teal
		successMostSubtle: lipgloss.Color("#a6e3a1"), // green

		// ANSI 16-color palette mirroring the terminal's own Catppuccin
		// Mocha palette, so bang-mode shell output looks the same inside
		// Harness as outside it.
		ansiBlack: lipgloss.Color("#45475a"), // surface1
		ansiRed:   lipgloss.Color("#f38ba8"), // red
		ansiGreen: lipgloss.Color("#a6e3a1"), // green

		// Subagent identity palette, in SubagentColorNames order.
		subagentPalette: [8]color.Color{
			lipgloss.Color("#f38ba8"), // red
			lipgloss.Color("#fab387"), // peach
			lipgloss.Color("#f9e2af"), // yellow
			lipgloss.Color("#a6e3a1"), // green
			lipgloss.Color("#89dceb"), // sky
			lipgloss.Color("#89b4fa"), // blue
			lipgloss.Color("#cba6f7"), // mauve
			lipgloss.Color("#f5c2e7"), // pink
		},
		ansiYellow:  lipgloss.Color("#f9e2af"), // yellow
		ansiBlue:    lipgloss.Color("#89b4fa"), // blue
		ansiMagenta: lipgloss.Color("#f5c2e7"), // pink
		ansiCyan:    lipgloss.Color("#94e2d5"), // teal
		ansiWhite:   lipgloss.Color("#bac2de"), // subtext1

		ansiBrightBlack:   lipgloss.Color("#585b70"), // surface2
		ansiBrightRed:     lipgloss.Color("#f38ba8"), // red
		ansiBrightGreen:   lipgloss.Color("#a6e3a1"), // green
		ansiBrightYellow:  lipgloss.Color("#f9e2af"), // yellow
		ansiBrightBlue:    lipgloss.Color("#89b4fa"), // blue
		ansiBrightMagenta: lipgloss.Color("#f5c2e7"), // pink
		ansiBrightCyan:    lipgloss.Color("#94e2d5"), // teal
		ansiBrightWhite:   lipgloss.Color("#a6adc8"), // subtext0
	}
}

// catppuccinMochaOverrides applies the Catppuccin Mocha tweaks that live
// outside the quickStyle token model: the bang (!) prompt and shell bar
// ride on mauve rather than the token defaults.
func catppuccinMochaOverrides(s Styles) Styles {
	s.Editor.PromptBangIconFocused = s.Editor.PromptBangIconFocused.
		Foreground(lipgloss.Color("#11111b")). // crust
		Background(lipgloss.Color("#cba6f7"))  // mauve
	s.Editor.PromptBangDotsFocused = s.Editor.PromptBangDotsFocused.
		Foreground(lipgloss.Color("#cba6f7")) // mauve
	s.Editor.PromptBangDotsBlurred = s.Editor.PromptBangDotsBlurred.
		Foreground(lipgloss.Color("#6c7086")) // overlay0

	s.Messages.ShellBarFocused = s.Messages.ShellBarFocused.
		BorderForeground(lipgloss.Color("#cba6f7")) // mauve
	s.Messages.ShellBarBlurred = s.Messages.ShellBarBlurred.
		BorderForeground(lipgloss.Color("#45475a")) // surface1
	s.Messages.ShellPrompt = s.Messages.ShellPrompt.
		Foreground(lipgloss.Color("#cba6f7")) // mauve
	s.Messages.ShellPromptBlurred = s.Messages.ShellPromptBlurred.
		Foreground(lipgloss.Color("#7f849c")) // overlay1

	return s
}

// gruvboxDarkOpts returns the quickStyleOpts palette for Gruvbox Dark,
// using canonical colors from the morhetz/gruvbox palette.
func gruvboxDarkOpts() quickStyleOpts {
	return quickStyleOpts{
		primary:   lipgloss.Color("#fabd2f"), // yellow
		secondary: lipgloss.Color("#d3869b"), // purple
		accent:    lipgloss.Color("#b8bb26"), // green
		keyword:   lipgloss.Color("#fe8019"), // orange

		fgBase:       lipgloss.Color("#ebdbb2"), // fg
		fgMoreSubtle: lipgloss.Color("#a89984"), // fg4/gray
		fgSubtle:     lipgloss.Color("#bdae93"), // fg3
		fgMostSubtle: lipgloss.Color("#928374"), // gray

		onPrimary: lipgloss.Color("#282828"), // bg on primary

		bgBase:         lipgloss.Color("#282828"), // bg
		bgLeastVisible: lipgloss.Color("#3c3836"), // bg1
		bgLessVisible:  lipgloss.Color("#504945"), // bg2
		bgMostVisible:  lipgloss.Color("#665c54"), // bg3

		separator: lipgloss.Color("#504945"), // bg2

		destructive:       lipgloss.Color("#fb4934"), // red bright
		error:             lipgloss.Color("#cc241d"), // red dark
		warningSubtle:     lipgloss.Color("#fabd2f"), // yellow bright
		warning:           lipgloss.Color("#d79921"), // yellow dark
		attention:         lipgloss.Color("#fe8019"), // orange
		busy:              lipgloss.Color("#fabd2f"), // yellow bright
		info:              lipgloss.Color("#83a598"), // blue bright
		infoMoreSubtle:    lipgloss.Color("#83a598"), // blue bright
		infoMostSubtle:    lipgloss.Color("#458588"), // blue dark
		success:           lipgloss.Color("#b8bb26"), // green bright
		successMoreSubtle: lipgloss.Color("#b8bb26"), // green bright
		successMostSubtle: lipgloss.Color("#8ec07c"), // aqua bright

		// ANSI 16-color palette for remapping raw terminal output
		// (e.g. bang-mode shell commands) onto legible Gruvbox colors.
		ansiBlack:   lipgloss.Color("#282828"),
		ansiRed:     lipgloss.Color("#cc241d"),
		ansiGreen:   lipgloss.Color("#98971a"),
		ansiYellow:  lipgloss.Color("#d79921"),
		ansiBlue:    lipgloss.Color("#458588"),
		ansiMagenta: lipgloss.Color("#b16286"),
		ansiCyan:    lipgloss.Color("#689d6a"),
		ansiWhite:   lipgloss.Color("#a89984"),

		ansiBrightBlack:   lipgloss.Color("#928374"),
		ansiBrightRed:     lipgloss.Color("#fb4934"),
		ansiBrightGreen:   lipgloss.Color("#b8bb26"),
		ansiBrightYellow:  lipgloss.Color("#fabd2f"),
		ansiBrightBlue:    lipgloss.Color("#83a598"),
		ansiBrightMagenta: lipgloss.Color("#d3869b"),
		ansiBrightCyan:    lipgloss.Color("#8ec07c"),
		ansiBrightWhite:   lipgloss.Color("#ebdbb2"),

		// Subagent identity palette, in SubagentColorNames order.
		subagentPalette: [8]color.Color{
			lipgloss.Color("#fb4934"), // bright red
			lipgloss.Color("#fe8019"), // orange
			lipgloss.Color("#fabd2f"), // bright yellow
			lipgloss.Color("#b8bb26"), // bright green
			lipgloss.Color("#8ec07c"), // bright aqua
			lipgloss.Color("#83a598"), // bright blue
			lipgloss.Color("#d3869b"), // bright purple
			lipgloss.Color("#f2cdcd"), // rosewater
		},
	}
}

// gruvboxDarkOverrides applies Gruvbox-specific tweaks on top of the
// token-driven base styles.
func gruvboxDarkOverrides(s Styles) Styles {
	// The shared quickStyle renders inline code as the destructive
	// (bright red) color on the code background. In Gruvbox that pairing
	// (#fb4934 on #504945) is only ~2.6:1 contrast, which is hard to
	// read. Use Gruvbox orange on the darkest background instead, which
	// keeps a warm "code" feel while clearing WCAG AA (~5.8:1).
	s.Markdown.Code.Color = hex(lipgloss.Color("#fe8019"))
	s.Markdown.Code.BackgroundColor = hex(lipgloss.Color("#282828"))
	return s
}

// builtinThemes maps theme names to their quickStyleOpts palette
// definitions. Adding a theme is one entry here (plus an optional
// overrides entry below); everything else in the UI resolves themes
// through [LoadTheme].
var builtinThemes = map[string]func() quickStyleOpts{
	"charmtone":        charmtoneOpts,
	"catppuccin-mocha": catppuccinMochaOpts,
	"gruvbox-dark":     gruvboxDarkOpts,
}

// builtinThemeOverrides maps theme names to functions that apply
// theme-specific style tweaks on top of the styles produced by
// [quickStyle]. Themes without overrides are absent from the map.
var builtinThemeOverrides = map[string]func(Styles) Styles{
	"charmtone":        charmtoneOverrides,
	"catppuccin-mocha": catppuccinMochaOverrides,
	"gruvbox-dark":     gruvboxDarkOverrides,
}

// BuiltinThemeNames returns the names of all built-in themes, sorted.
func BuiltinThemeNames() []string {
	names := make([]string, 0, len(builtinThemes))
	for name := range builtinThemes {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// LoadTheme loads a theme by name, case-insensitively. The empty name
// yields the default Charmtone theme. Unknown names return an error
// listing the available themes.
func LoadTheme(name string) (Styles, error) {
	if name == "" {
		return CharmtonePantera(), nil
	}
	key := strings.ToLower(name)
	optsFn, ok := builtinThemes[key]
	if !ok {
		return Styles{}, fmt.Errorf("unknown theme %q; available themes: %s", name, strings.Join(BuiltinThemeNames(), ", "))
	}
	s := quickStyle(optsFn())
	if override, ok := builtinThemeOverrides[key]; ok {
		s = override(s)
	}
	return s, nil
}

// ThemeFromConfig resolves a configured theme name, falling back to the
// default Charmtone theme when the name is empty or unknown.
func ThemeFromConfig(name string) Styles {
	s, err := LoadTheme(name)
	if err != nil {
		return CharmtonePantera()
	}
	return s
}

// ThemeSwatch returns a handful of representative colors for the named
// theme, in a stable order (primary, secondary, accent, keyword). It is
// meant for previews — the theme picker paints these next to each entry —
// and returns nil for unknown names. Lookups are case-insensitive, like
// [LoadTheme].
func ThemeSwatch(name string) []color.Color {
	optsFn, ok := builtinThemes[strings.ToLower(name)]
	if !ok {
		return nil
	}
	opts := optsFn()
	return []color.Color{opts.primary, opts.secondary, opts.accent, opts.keyword}
}
