package cmd

import (
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

type panickingModel struct{}

func (m *panickingModel) Init() (cmd tea.Cmd) {
	panic("init blew up")
}

func (m *panickingModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m, nil
}

func (m *panickingModel) View() tea.View {
	return tea.NewView("")
}

func TestPanicCapturingModelCapturesThenReraises(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HARNESS_CRASH_DIR", dir)

	model := newPanicCapturingModel(&panickingModel{})
	require.PanicsWithValue(t, "init blew up", func() {
		model.Init()
	})

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	require.NoError(t, err)
	require.Contains(t, string(data), "component: tui\n")
	require.Contains(t, string(data), "panic: init blew up\n")
	require.Contains(t, string(data), "panickingModel")
}

type cmdPanickingModel struct{}

func (m cmdPanickingModel) Init() tea.Cmd { return nil }

func (m cmdPanickingModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m, func() tea.Msg {
		panic("cmd blew up")
	}
}

func (m cmdPanickingModel) View() tea.View {
	return tea.NewView("")
}

func TestPanicCapturingModelWrapsReturnedCmd(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HARNESS_CRASH_DIR", dir)

	wrapped := newPanicCapturingModel(cmdPanickingModel{})
	_, cmd := wrapped.Update(nil)
	require.NotNil(t, cmd)
	require.PanicsWithValue(t, "cmd blew up", func() {
		cmd()
	})

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	require.NoError(t, err)
	require.Contains(t, string(data), "panic: cmd blew up\n")
}
