package config

import (
	"log/slog"
	"slices"
)

// enumOption is an option with a closed set of values. A value outside
// the set is a typo the loader would otherwise pass straight to the UI,
// where each reader falls back in its own way (dialog_placement: Top
// silently anchors at the bottom).
type enumOption struct {
	// path is the option's config path, for the warning.
	path string
	// field is the option's value in a loaded config.
	field *string
	// allowed is every value the option takes. The schema's enum for the
	// option must list exactly these; TestEnumOptionsMatchSchema holds it
	// to that.
	allowed []string
}

// enumOptions lists the closed-set options of c. c.Options and
// c.Options.TUI must be allocated.
func (c *Config) enumOptions() []enumOption {
	tui := c.Options.TUI
	return []enumOption{
		{path: "options.tui.diff_mode", field: &tui.DiffMode, allowed: []string{DiffModeUnified, DiffModeSplit}},
		{path: "options.tui.scrollbar", field: &tui.Scrollbar, allowed: []string{ScrollbarDefault, ScrollbarAlways, ScrollbarNever}},
		{path: "options.tui.exit_banner", field: (*string)(&tui.ExitBanner), allowed: []string{string(ExitBannerDefault), string(ExitBannerCompact), string(ExitBannerNone)}},
		{path: "options.tui.dialog_placement", field: &tui.DialogPlacement, allowed: []string{DialogPlacementBottom, DialogPlacementTop}},
		{path: "options.notifications", field: &c.Options.Notifications, allowed: []string{NotificationsAuto, NotificationsNative, NotificationsOSC, NotificationsBell, NotificationsDisabled}},
	}
}

// clearUnknownEnumValues resets every closed-set option holding a value
// outside its set to unset, which each reader treats as its default, and
// says so. Unset stays unset.
func (c *Config) clearUnknownEnumValues() {
	for _, opt := range c.enumOptions() {
		if *opt.field == "" || slices.Contains(opt.allowed, *opt.field) {
			continue
		}
		slog.Warn("Config option has an unknown value; using its default", "option", opt.path, "value", *opt.field, "allowed", opt.allowed)
		*opt.field = ""
	}
}
