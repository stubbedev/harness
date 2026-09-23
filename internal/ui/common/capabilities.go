package common

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	xstrings "github.com/charmbracelet/x/exp/strings"

	"github.com/stubbedev/harness/internal/ui/notification"
)

// Capabilities define different terminal capabilities supported.
type Capabilities struct {
	// Env is the terminal environment variables.
	Env uv.Environ
	// ReportFocusEvents indicates whether the terminal supports focus events.
	ReportFocusEvents bool
	// OSC99Notifications indicates whether the terminal supports OSC 99 notifications.
	OSC99Notifications bool
}

// Update updates the capabilities based on the given message.
func (c *Capabilities) Update(msg any) {
	switch m := msg.(type) {
	case tea.EnvMsg:
		c.Env = uv.Environ(m)
	case tea.ModeReportMsg:
		switch m.Mode {
		case ansi.ModeFocusEvent:
			c.ReportFocusEvents = modeSupported(m.Value)
		}
	case uv.UnknownOscEvent:
		if notification.DetectOSC99Support(string(m)) {
			c.OSC99Notifications = true
		}
	}
}

// QueryCmd returns a [tea.Cmd] that queries the terminal for different
// capabilities.
func QueryCmd(env uv.Environ) tea.Cmd {
	var sb strings.Builder
	sb.WriteString(ansi.RequestPrimaryDeviceAttributes)
	sb.WriteString(ansi.QueryModifyOtherKeys)
	sb.WriteString(ansi.RequestModeFocusEvent)
	sb.WriteString(notification.OSC99QuerySequence())

	// Queries that should only be sent to "smart" normal terminals.
	shouldQueryFor := shouldQueryCapabilities(env)
	if shouldQueryFor {
		sb.WriteString(ansi.RequestNameVersion)
	}

	return tea.Raw(sb.String())
}

func modeSupported(v ansi.ModeSetting) bool {
	return v.IsSet() || v.IsReset()
}

// kittyTerminals defines terminals supporting querying capabilities.
var kittyTerminals = []string{"alacritty", "ghostty", "kitty", "rio", "wezterm"}

func shouldQueryCapabilities(env uv.Environ) bool {
	const osVendorTypeApple = "Apple"
	termType := env.Getenv("TERM")
	termProg, okTermProg := env.LookupEnv("TERM_PROGRAM")
	_, okSSHTTY := env.LookupEnv("SSH_TTY")
	if okTermProg && strings.Contains(termProg, osVendorTypeApple) {
		return false
	}
	return (!okTermProg && !okSSHTTY) ||
		(!strings.Contains(termProg, osVendorTypeApple) && !okSSHTTY) ||
		// Terminals that do support XTVERSION.
		xstrings.ContainsAnyOf(termType, kittyTerminals...)
}
