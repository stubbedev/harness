package dialog

import (
	"fmt"
	"image"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/csync"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/list"
	"github.com/stubbedev/harness/internal/ui/styles"
)

var shortTerminalRows = []int{10, 7}

const (
	tallTerminalRows = 80
	fitTerminalCols  = 100
	fitListLength    = 30
)

type pickerFixture struct {
	name  string
	long  func(t *testing.T) Dialog
	short func(t *testing.T) Dialog
}

func pickerFixtures() []pickerFixture {
	notifications := func(*testing.T) Dialog { return NewNotifications(paletteTestCommon()) }
	return []pickerFixture{
		{
			name: "themes",
			long: func(t *testing.T) Dialog { return newTestThemes(t, "") },
			short: func(t *testing.T) Dialog {
				d := newTestThemes(t, "")
				typeFilter(d, "gruvbox")
				return d
			},
		},
		{
			name: "connect",
			long: func(t *testing.T) Dialog {
				return newConnectDialogForTest(t, configuredTestConfig(t), manyConnectProviders(fitListLength))
			},
			short: func(t *testing.T) Dialog {
				return newConnectDialogForTest(t, configuredTestConfig(t), manyConnectProviders(3))
			},
		},
		{name: "notifications", long: notifications, short: notifications},
		{name: "reasoning", long: newReasoningForFitTest, short: newReasoningForFitTest},
		{
			name:  "rewind",
			long:  newLongRewindForTest,
			short: func(t *testing.T) Dialog { return newRewindForTest(t) },
		},
		{
			name: "servers",
			long: func(*testing.T) Dialog {
				return NewMCPServers(newManyServersCommon(), map[string]mcp.ClientInfo{})
			},
			short: func(*testing.T) Dialog {
				return NewMCPServers(newServersTestCommon(), map[string]mcp.ClientInfo{})
			},
		},
		{
			name: "commands",
			long: func(t *testing.T) Dialog { return newShortcutPalette(t) },
			short: func(t *testing.T) Dialog {
				d := newShortcutPalette(t)
				typeFilter(d, "session")
				return d
			},
		},
	}
}

func typeFilter(d Dialog, s string) {
	for _, r := range s {
		d.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func manyConnectProviders(n int) []catalog.Provider {
	providers := make([]catalog.Provider, n)
	for i := range providers {
		id := fmt.Sprintf("provider-%02d", i)
		providers[i] = catalog.Provider{
			ID:     catalog.InferenceProvider(id),
			Name:   id,
			Type:   catalog.TypeOpenAI,
			Models: []catalog.Model{{ID: id + "-model", Name: "Model"}},
		}
	}
	return providers
}

func newReasoningForFitTest(t *testing.T) Dialog {
	t.Helper()
	cfg := &config.Config{
		Agents: map[string]config.Agent{config.AgentCoder: {Model: config.SelectedModelTypeLarge}},
		Models: map[config.SelectedModelType]config.SelectedModel{
			config.SelectedModelTypeLarge: {Provider: "p", Model: "m"},
		},
		Providers: csync.NewMapFrom(map[string]config.ProviderConfig{"p": {
			ID:     "p",
			Models: []catalog.Model{{ID: "m", ReasoningLevels: []string{"minimal", "low", "medium", "high", "xhigh", "max"}}},
		}}),
	}
	s := styles.CharmtonePantera()
	d, err := NewReasoning(&common.Common{Workspace: &stubWorkspace{cfg: cfg}, Styles: &s})
	require.NoError(t, err)
	return d
}

func newLongRewindForTest(t *testing.T) Dialog {
	t.Helper()
	messages := make([]message.Message, fitListLength)
	for i := range messages {
		messages[i] = message.Message{
			ID:        fmt.Sprintf("m%02d", i),
			Role:      message.User,
			Parts:     []message.ContentPart{message.TextContent{Text: fmt.Sprintf("turn %02d", i)}},
			CreatedAt: time.Now().Add(-time.Duration(i) * time.Minute).Unix(),
		}
	}
	st := styles.CharmtonePantera()
	r, err := NewRewind(&common.Common{Styles: &st, Workspace: &rewindWorkspace{messages: messages}}, "s1")
	require.NoError(t, err)
	return r
}

func newManyServersCommon() *common.Common {
	st := styles.CharmtonePantera()
	mcps := config.MCPs{}
	for i := range fitListLength {
		mcps[fmt.Sprintf("server-%02d", i)] = config.MCPConfig{Type: config.MCPStdio, Command: "srv"}
	}
	return &common.Common{Styles: &st, Workspace: &serversWorkspace{cfg: &config.Config{MCP: mcps}}}
}

func drawDialogRows(d Dialog, cols, rows int) []string {
	scr := uv.NewScreenBuffer(cols, rows)
	d.Draw(scr, image.Rect(0, 0, cols, rows))
	out := make([]string, rows)
	for y := range rows {
		out[y] = ansi.Strip(scr.Line(y).Render())
	}
	return out
}

func rowIndex(rows []string, text string) int {
	for i, row := range rows {
		if strings.Contains(row, text) {
			return i
		}
	}
	return -1
}

func isRule(row string) bool {
	trimmed := strings.TrimSpace(row)
	return trimmed != "" && strings.Trim(trimmed, "─") == ""
}

func requireDialogFits(t *testing.T, rows []string) {
	t.Helper()
	dump := strings.Join(rows, "\n")
	closing := -1
	for i, row := range rows {
		if isRule(row) {
			closing = i
		}
	}
	require.GreaterOrEqual(t, closing, 2, "no panel drawn:\n%s", dump)
	require.False(t, isRule(rows[closing-1]), "the input row was clipped:\n%s", dump)
	require.True(t, isRule(rows[closing-2]), "the closing rule was clipped:\n%s", dump)
	require.True(t, isRule(rows[0]) || strings.TrimSpace(rows[0]) == "", "the panel top was clipped:\n%s", dump)
}

func TestPickersFitShortTerminal(t *testing.T) {
	t.Parallel()

	for _, fx := range pickerFixtures() {
		for _, rows := range shortTerminalRows {
			t.Run(fmt.Sprintf("%s/%d", fx.name, rows), func(t *testing.T) {
				t.Parallel()
				requireDialogFits(t, drawDialogRows(fx.long(t), fitTerminalCols, rows))
			})
		}
	}
}

func TestPickersFitShortTerminalInDetailPhase(t *testing.T) {
	t.Parallel()

	for _, rows := range shortTerminalRows {
		rewind, ok := newLongRewindForTest(t).(*Rewind)
		require.True(t, ok)
		rewind.list.SetSelected(0)
		require.Nil(t, rewind.confirmSelection())
		require.Equal(t, pickerPhaseDetail, rewind.phase)
		requireDialogFits(t, drawDialogRows(rewind, fitTerminalCols, rows))

		servers := NewMCPServers(newManyServersCommon(), map[string]mcp.ClientInfo{})
		require.Nil(t, servers.confirmSelection())
		require.Equal(t, pickerPhaseDetail, servers.phase)
		requireDialogFits(t, drawDialogRows(servers, fitTerminalCols, rows))
	}
}

func TestPickersShowFullListInTallTerminal(t *testing.T) {
	t.Parallel()

	for _, fx := range pickerFixtures() {
		t.Run(fx.name, func(t *testing.T) {
			t.Parallel()
			d := fx.short(t)
			labels := visibleLabels(t, d)
			rows := drawDialogRows(d, fitTerminalCols, tallTerminalRows)
			requireDialogFits(t, rows)
			for _, label := range labels {
				require.GreaterOrEqual(t, rowIndex(rows, label), 0, "%q missing:\n%s", label, strings.Join(rows, "\n"))
			}
		})
	}
}

func TestCommandsPaletteDrawsScrollbarWhenClipped(t *testing.T) {
	t.Parallel()

	rows := drawDialogRows(newShortcutPalette(t), fitTerminalCols, shortTerminalRows[0])
	require.GreaterOrEqual(t, rowIndex(rows, styles.ScrollbarThumb), 0, "a clipped palette must show a scrollbar:\n%s", strings.Join(rows, "\n"))
}

func visibleLabels(t *testing.T, d Dialog) []string {
	t.Helper()
	var labels []string
	switch d := d.(type) {
	case *Themes:
		labels = filterableLabels(t, d.list.FilteredItems())
	case *Connect:
		labels = filterableLabels(t, d.list.FilteredItems())
	case *simplePicker[string]:
		labels = filterableLabels(t, d.list.FilteredItems())
	case *Rewind:
		labels = filterableLabels(t, d.list.FilteredItems())
	case *MCPServers:
		labels = filterableLabels(t, d.list.FilteredItems())
	case *Commands:
		labels = filterableLabels(t, d.list.FilteredItems())
	default:
		t.Fatalf("no label reader for %T", d)
	}
	require.NotEmpty(t, labels)
	return labels
}

func filterableLabels(t *testing.T, items []list.Item) []string {
	t.Helper()
	labels := make([]string, 0, len(items))
	for _, it := range items {
		item, ok := it.(PickerItem)
		require.True(t, ok, "%T is not a picker row", it)
		labels = append(labels, item.Label())
	}
	return labels
}
