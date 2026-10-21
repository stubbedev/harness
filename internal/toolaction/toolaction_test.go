package toolaction

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

type color string

type params struct {
	Color color  `json:"color"`
	Note  string `json:"note,omitempty" description:"free text"`
}

func newTable() Table[color, int] {
	return New("color", map[color]int{"red": 1, "blue": 2, "green": 3})
}

func newTool() fantasy.AgentTool {
	return fantasy.NewParallelAgentTool("paint", "paints", func(context.Context, params, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		return fantasy.NewTextResponse("ok"), nil
	})
}

func TestTableLookup(t *testing.T) {
	t.Parallel()

	table := newTable()
	got, err := table.Lookup("green")
	require.NoError(t, err)
	require.Equal(t, 3, got)

	_, err = table.Lookup("mauve")
	require.EqualError(t, err, `unknown color "mauve"; available: blue, green, red`)
}

func TestTableNamesAreSortedCopies(t *testing.T) {
	t.Parallel()

	table := newTable()
	names := table.Names()
	require.Equal(t, []string{"blue", "green", "red"}, names)
	names[0] = "changed"
	require.Equal(t, []string{"blue", "green", "red"}, table.Names())
	require.Equal(t, Enum{Field: "color", Values: []string{"blue", "green", "red"}}, table.Enum())
}

func TestAdvertiseSetsEnumWithoutTouchingTheRest(t *testing.T) {
	t.Parallel()

	tool := newTool()
	advertised := Advertise(tool, newTable().Enum())

	info := advertised.Info()
	require.Equal(t, map[string]any{"type": "string", "enum": []string{"blue", "green", "red"}}, info.Parameters["color"])
	require.Equal(t, tool.Info().Parameters["note"], info.Parameters["note"])
	require.Equal(t, tool.Info().Required, info.Required)
	require.True(t, info.Parallel)
	require.NotContains(t, tool.Info().Parameters["color"], "enum")

	resp, err := advertised.Run(t.Context(), fantasy.ToolCall{Input: `{"color":"red"}`})
	require.NoError(t, err)
	require.Equal(t, "ok", resp.Content)
}

func TestAdvertisePanicsOnUnknownParameter(t *testing.T) {
	t.Parallel()

	require.PanicsWithValue(t, `toolaction: tool paint has no parameter "shade"`, func() {
		Advertise(newTool(), Enum{Field: "shade", Values: []string{"dark"}})
	})
}
