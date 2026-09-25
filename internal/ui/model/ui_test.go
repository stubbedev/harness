package model

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/csync"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/workspace"
)

func TestCurrentModelSupportsImages(t *testing.T) {
	t.Parallel()

	t.Run("returns false when config is nil", func(t *testing.T) {
		t.Parallel()

		ui := newTestUIWithConfig(t, nil)
		require.False(t, ui.currentModelSupportsImages())
	})

	t.Run("returns false when coder agent is missing", func(t *testing.T) {
		t.Parallel()

		cfg := &config.Config{
			Providers: csync.NewMap[string, config.ProviderConfig](),
			Agents:    map[string]config.Agent{},
		}
		ui := newTestUIWithConfig(t, cfg)
		require.False(t, ui.currentModelSupportsImages())
	})

	t.Run("returns false when model is not found", func(t *testing.T) {
		t.Parallel()

		cfg := &config.Config{
			Providers: csync.NewMap[string, config.ProviderConfig](),
			Agents: map[string]config.Agent{
				config.AgentCoder: {Model: config.SelectedModelTypeLarge},
			},
		}
		ui := newTestUIWithConfig(t, cfg)
		require.False(t, ui.currentModelSupportsImages())
	})

	t.Run("returns true when current model supports images", func(t *testing.T) {
		t.Parallel()

		providers := csync.NewMap[string, config.ProviderConfig]()
		providers.Set("test-provider", config.ProviderConfig{
			ID: "test-provider",
			Models: []catalog.Model{
				{ID: "test-model", SupportsImages: true},
			},
		})

		cfg := &config.Config{
			Models: map[config.SelectedModelType]config.SelectedModel{
				config.SelectedModelTypeLarge: {
					Provider: "test-provider",
					Model:    "test-model",
				},
			},
			Providers: providers,
			Agents: map[string]config.Agent{
				config.AgentCoder: {Model: config.SelectedModelTypeLarge},
			},
		}

		ui := newTestUIWithConfig(t, cfg)
		require.True(t, ui.currentModelSupportsImages())
	})
}

func TestMouseMode(t *testing.T) {
	t.Parallel()

	t.Run("returns no mouse mode when disabled", func(t *testing.T) {
		t.Parallel()

		require.Equal(t, tea.MouseModeNone, mouseMode(false, false))
		require.Equal(t, tea.MouseModeNone, mouseMode(false, true))
	})

	t.Run("returns cell motion when enabled and no inline editor is active", func(t *testing.T) {
		t.Parallel()

		require.Equal(t, tea.MouseModeCellMotion, mouseMode(true, false))
	})

	t.Run("returns all motion when enabled and an inline editor is active", func(t *testing.T) {
		t.Parallel()

		require.Equal(t, tea.MouseModeAllMotion, mouseMode(true, true))
	})
}

func newTestUIWithConfig(t *testing.T, cfg *config.Config) *UI {
	t.Helper()

	ws := &testWorkspace{cfg: cfg}
	styles := common.ThemeStylesForWorkspace(ws)
	return &UI{
		com: &common.Common{
			Workspace: ws,
			Styles:    &styles,
		},
	}
}

// testWorkspace is a minimal [workspace.Workspace] stub for unit tests.
type testWorkspace struct {
	workspace.Workspace
	cfg *config.Config
	// steeredSession/steeredText record the last SteerAgent call.
	steeredSession string
	steeredText    string
}

func (w *testWorkspace) Config() *config.Config {
	return w.cfg
}

func (w *testWorkspace) WorkingDir() string {
	return "/tmp/harness-test"
}

func (w *testWorkspace) CreateAgentToolSessionID(messageID, toolCallID string) string {
	return "agent-tool-" + messageID + "-" + toolCallID
}

func (w *testWorkspace) ParseAgentToolSessionID(sessionID string) (string, string, bool) {
	rest, ok := strings.CutPrefix(sessionID, "agent-tool-")
	if !ok {
		return "", "", false
	}
	_, toolCallID, found := strings.Cut(rest, "-")
	return "", toolCallID, found
}

func (w *testWorkspace) AgentIsReady() bool {
	return false
}

// AgentCancelTurn satisfies the busy-cancel path; the embedded nil
// Workspace would panic, and stress tests press escape while busy.
func (w *testWorkspace) AgentCancelTurn(string) {}

func (w *testWorkspace) ListMessages(context.Context, string) ([]message.Message, error) {
	return nil, nil
}

// SteerAgent records the steering request so tests can assert on it.
func (w *testWorkspace) SteerAgent(_ context.Context, childSessionID, text string) error {
	w.steeredSession = childSessionID
	w.steeredText = text
	return nil
}
