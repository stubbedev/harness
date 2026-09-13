package common

import (
	"errors"
	"fmt"
	"image"
	"os"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stubbedev/harness/internal/clipboard"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/ui/styles"
	"github.com/stubbedev/harness/internal/ui/util"
	"github.com/stubbedev/harness/internal/workspace"
)

// MaxAttachmentSize defines the maximum allowed size for file attachments (5 MB).
const MaxAttachmentSize = int64(5 * 1024 * 1024)

// AllowedImageTypes defines the permitted image file types.
var AllowedImageTypes = []string{".jpg", ".jpeg", ".png"}

// IsImagePath reports whether the given path has one of the allowed image
// file extensions.
func IsImagePath(path string) bool {
	lowerPath := strings.ToLower(path)
	return slices.ContainsFunc(AllowedImageTypes, func(ext string) bool {
		return strings.HasSuffix(lowerPath, ext)
	})
}

// Common defines common UI options and configurations.
type Common struct {
	Workspace workspace.Workspace
	Styles    *styles.Styles
}

// Config returns the pure-data configuration associated with this [Common] instance.
func (c *Common) Config() *config.Config {
	return c.Workspace.Config()
}

// DefaultCommon returns the default common UI configurations. A theme
// configured via options.tui.theme wins; otherwise the theme is chosen
// from the large model's provider, falling back to the default theme.
func DefaultCommon(ws workspace.Workspace) *Common {
	s := ThemeStylesForWorkspace(ws)
	return &Common{
		Workspace: ws,
		Styles:    &s,
	}
}

// largeModelProviderID returns the provider ID of the currently selected
// large model, or the empty string if none is set or the workspace is nil.
func largeModelProviderID(ws workspace.Workspace) string {
	if ws == nil {
		return ""
	}
	cfg := ws.Config()
	if cfg == nil {
		return ""
	}
	return cfg.Models[config.SelectedModelTypeLarge].Provider
}

// ThemeNameFromConfig extracts the configured theme name from config,
// returning "" (which LoadTheme treats as the default) when config is
// nil or no theme is set.
func ThemeNameFromConfig(cfg *config.Config) string {
	if cfg == nil || cfg.Options == nil || cfg.Options.TUI == nil {
		return ""
	}
	return cfg.Options.TUI.Theme
}

// ThemeStylesForWorkspace resolves the theme for a workspace: the
// configured theme (options.tui.theme) takes precedence over the
// provider-based mapping.
func ThemeStylesForWorkspace(ws workspace.Workspace) styles.Styles {
	var cfg *config.Config
	if ws != nil {
		cfg = ws.Config()
	}
	return ThemeStylesForConfig(cfg, largeModelProviderID(ws))
}

// ThemeStylesForConfig resolves the theme for a config plus a fallback
// provider ID: a theme configured via options.tui.theme wins, otherwise
// the provider mapping decides.
func ThemeStylesForConfig(cfg *config.Config, providerID string) styles.Styles {
	if name := ThemeNameFromConfig(cfg); name != "" {
		return styles.ThemeFromConfig(name)
	}
	return styles.ThemeForProvider(providerID)
}

// CenterRect returns a new [Rectangle] centered within the given area with the
// specified width and height.
func CenterRect(area uv.Rectangle, width, height int) uv.Rectangle {
	centerX := area.Min.X + area.Dx()/2
	centerY := area.Min.Y + area.Dy()/2
	minX := centerX - width/2
	minY := centerY - height/2
	maxX := minX + width
	maxY := minY + height
	return image.Rect(minX, minY, maxX, maxY)
}

// BottomLeftRect returns a new [Rectangle] positioned at the bottom-left within the given area with the
// specified width and height.
func BottomLeftRect(area uv.Rectangle, width, height int) uv.Rectangle {
	minX := area.Min.X
	maxX := minX + width
	maxY := area.Max.Y
	minY := maxY - height
	return image.Rect(minX, minY, maxX, maxY)
}

// IsFileTooBig checks if the file at the given path exceeds the specified size
// limit.
func IsFileTooBig(filePath string, sizeLimit int64) (bool, error) {
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return false, fmt.Errorf("error getting file info: %w", err)
	}

	if fileInfo.Size() > sizeLimit {
		return true, nil
	}

	return false, nil
}

// CopyToClipboard copies the given text to the clipboard using both OSC 52
// (terminal escape sequence) and native clipboard for maximum compatibility.
// Returns a command that reports the outcome to the user, using the given
// message on success.
func CopyToClipboard(text, successMessage string) tea.Cmd {
	return CopyToClipboardWithCallback(text, successMessage, nil)
}

// CopyToClipboardWithCallback copies text to clipboard and executes a callback
// before showing the success message.
// This is useful when you need to perform additional actions like clearing UI state.
//
// The callback and the success message only run when the copy is believed to
// have worked, so callers can safely use the callback to discard the copied
// state (a selection, say) without losing it on a failed copy.
func CopyToClipboardWithCallback(text, successMessage string, callback tea.Cmd) tea.Cmd {
	return tea.Sequence(
		tea.SetClipboard(text),
		func() tea.Msg {
			// OSC 52 above is fire and forget: the terminal never answers, so a
			// platform without a native clipboard (an SSH session, say) gets the
			// benefit of the doubt. Only a native clipboard that accepted the
			// write and then does not hold the text is a real failure.
			if err := clipboard.WriteText(text); errors.Is(err, clipboard.ErrWriteFailed) {
				return util.NewWarnMsg("Failed to copy to clipboard")
			}
			return tea.Sequence(callback, util.ReportInfo(successMessage))()
		},
	)
}
