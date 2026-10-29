package model

import (
	"context"
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
			Providers: csync.NewMap[catalog.InferenceProvider, config.ProviderConfig](),
			Agents:    map[string]config.Agent{},
		}
		ui := newTestUIWithConfig(t, cfg)
		require.False(t, ui.currentModelSupportsImages())
	})

	t.Run("returns false when model is not found", func(t *testing.T) {
		t.Parallel()

		cfg := &config.Config{
			Providers: csync.NewMap[catalog.InferenceProvider, config.ProviderConfig](),
			Agents: map[string]config.Agent{
				config.AgentCoder: {Model: config.SelectedModelTypeLarge},
			},
		}
		ui := newTestUIWithConfig(t, cfg)
		require.False(t, ui.currentModelSupportsImages())
	})

	t.Run("returns true when current model supports images", func(t *testing.T) {
		t.Parallel()

		providers := csync.NewMap[catalog.InferenceProvider, config.ProviderConfig]()
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

// TestItemEnvCarriesWorkingDir pins the source of the paths a
// transcript row shows: the workspace the session belongs to, not the
// process directory, so client/server mode labels the server's files
// correctly. A UI without a workspace falls back to home-shortening.
func TestItemEnvCarriesWorkingDir(t *testing.T) {
	t.Parallel()

	u := newFrameTestUI(t)
	require.Equal(t, u.com.Workspace.WorkingDir(), u.itemEnv().WorkingDir)

	require.Empty(t, newTestUI().itemEnv().WorkingDir)
}

func TestMouseMode(t *testing.T) {
	t.Parallel()

	t.Run("returns no mouse mode when disabled", func(t *testing.T) {
		t.Parallel()

		require.Equal(t, tea.MouseModeNone, mouseMode(false))
	})

	t.Run("returns all motion when enabled", func(t *testing.T) {
		t.Parallel()

		// Hover affordances need motion with no button pressed, so the
		// cell-motion mode is not enough.
		require.Equal(t, tea.MouseModeAllMotion, mouseMode(true))
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
	// messages backs ListMessages, keyed by session ID; listed records
	// the IDs ListMessages was asked for.
	messages map[string][]message.Message
	listed   []string
	// steeredSession/steeredText record the last SteerAgent call.
	steeredSession string
	steeredText    string
	// jobs backs RunningJobs for the tasks strip's job rows.
	jobs []workspace.RunningJobInfo
}

func (w *testWorkspace) Config() *config.Config {
	return w.cfg
}

func (w *testWorkspace) WorkingDir() string {
	return "/tmp/harness-test"
}

func (w *testWorkspace) AgentIsReady() bool {
	return false
}

func (w *testWorkspace) AgentReadyErr() error {
	return workspace.ErrAgentNotInitialized
}

func (w *testWorkspace) AgentCancel(string) error { return nil }

func (w *testWorkspace) ListMessages(_ context.Context, sessionID string) ([]message.Message, error) {
	w.listed = append(w.listed, sessionID)
	return w.messages[sessionID], nil
}

// SteerAgent records the steering request so tests can assert on it.
func (w *testWorkspace) SteerAgent(_ context.Context, childSessionID, text string) error {
	w.steeredSession = childSessionID
	w.steeredText = text
	return nil
}

func (w *testWorkspace) RunningJobs(string) []workspace.RunningJobInfo {
	return w.jobs
}
