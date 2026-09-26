package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/styles"
)

func groupStyles() *styles.Styles {
	s := styles.ThemeForProvider("")
	return &s
}

func bashTool(id, command string, finished bool) ToolMessageItem {
	input := `{"command":` + quoteJSON(command) + `}`
	return NewToolMessageItem(groupStyles(), "msg", message.ToolCall{
		ID: id, Name: "shell", Input: input, Finished: finished,
	}, nil, false)
}

func quoteJSON(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func TestToolCallSummary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tc   message.ToolCall
		want string
	}{
		{
			name: "bash command keeps the first line",
			tc:   message.ToolCall{Name: "shell", Input: `{"command":"git status\n--short"}`},
			want: "git status",
		},
		{
			name: "file path",
			tc:   message.ToolCall{Name: "Edit", Input: `{"file_path":"/a/b.go","new_string":"x"}`},
			want: "/a/b.go",
		},
		{
			name: "falls back to the first string value",
			tc:   message.ToolCall{Name: "Whatever", Input: `{"whatever":"value"}`},
			want: "value",
		},
		{
			name: "fallback follows key order rather than input order",
			tc:   message.ToolCall{Name: "Whatever", Input: `{"zebra":"last","alpha":"first","middle":"other"}`},
			want: "first",
		},
		{
			name: "fallback skips non-string and blank values",
			tc:   message.ToolCall{Name: "Whatever", Input: `{"a":null,"b":3,"c":false,"d":[],"e":{},"f":" \t","g":"first\nsecond","h":"other"}`},
			want: "first",
		},
		{
			name: "preferred keys retain priority over fallback and input order",
			tc:   message.ToolCall{Name: "Whatever", Input: `{"query":"query","path":"path","alpha":"fallback","command":"  run   tests\nnext"}`},
			want: "run tests",
		},
		{
			name: "unusable preferred keys allow fallback",
			tc:   message.ToolCall{Name: "Whatever", Input: `{"command":false,"query":" \t","zebra":"last","alpha":"first"}`},
			want: "first",
		},
		{
			name: "invalid input",
			tc:   message.ToolCall{Name: "Whatever", Input: `{"alpha":`},
			want: "",
		},
		{
			name: "no string values",
			tc:   message.ToolCall{Name: "Whatever", Input: `{"count":3,"enabled":true}`},
			want: "",
		},
		{
			name: "no usable input",
			tc:   message.ToolCall{Name: "View", Input: `{}`},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for range 100 {
				require.Equal(t, tt.want, ToolCallSummary(tt.tc))
			}
		})
	}
}

func TestToolLabelsStableAcrossRenders(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		input string
		want  string
	}{
		{"memory", `{"action":"search","query":"build commands","category":"project"}`, "Memory build commands"},
		{"memory", `{"title":"Build commands","content":"Run go test\nThen lint","category":"project","action":"save","pinned":true}`, "Memory save"},
		{"custom_tool", `{"zebra":"last","alpha":"first\nsecond","middle":"other"}`, "Custom Tool first"},
		{"custom_tool", `{"alpha":"first\nsecond","middle":"other","zebra":"last"}`, "Custom Tool first"},
	} {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			sty := groupStyles()
			call := message.ToolCall{ID: "t1", Name: tt.name, Input: tt.input, Finished: true}
			item := NewToolMessageItem(sty, "msg", call, nil, false)
			singleton := NewToolGroupMessageItem(sty, item)
			group := NewToolGroupMessageItem(sty, item)
			group.AddTool(bashTool("t2", "go test", true))
			require.True(t, group.ToggleExpanded())
			require.True(t, group.Spinning())
			wantGroup := "Running (2 tool calls)\n " + tt.want + "\n Shell go test"

			for range 100 {
				require.Equal(t, tt.want, ansi.Strip(ToolOneLiner(sty, item, 120, false)))
				require.Equal(t, tt.want, ansi.Strip(singleton.RawRender(120)))
				require.Equal(t, tt.want, strings.TrimSpace(ansi.Strip(singleton.Render(120))))
				require.Equal(t, wantGroup, ansi.Strip(group.RawRender(120)))
				require.Contains(t, ansi.Strip(group.Render(120)), tt.want)
				group.Advance()
			}

			call.Input = `{"query":"updated parameters"}`
			item.SetToolCall(call)
			wantUpdated := callLabel(call) + " updated parameters"
			require.Equal(t, wantUpdated, ansi.Strip(ToolOneLiner(sty, item, 120, false)))
			require.Contains(t, ansi.Strip(group.Render(120)), wantUpdated)
		})
	}
}

func TestToolGroupRenderLevels(t *testing.T) {
	t.Parallel()
	sty := groupStyles()

	done := func(id string) ToolMessageItem {
		item := bashTool(id, "gh issue close 16 --repo x --comment \"Fixed: unpresentable schemas (wider", true)
		res := message.ToolResult{ToolCallID: id, Name: "shell", Content: "ok"}
		item.SetResult(&res)
		return item
	}

	t.Run("singleton renders the bare one-liner", func(t *testing.T) {
		t.Parallel()
		g := NewToolGroupMessageItem(sty, done("t1"))
		out := ansi.Strip(g.Render(80))
		assert.Contains(t, out, "Shell")
		assert.Contains(t, out, "gh issue close 16")
		assert.NotContains(t, out, "tool call")
		assert.Equal(t, 1, strings.Count(out, "\n")+1)
	})

	t.Run("expanded singleton renders as the bare call", func(t *testing.T) {
		t.Parallel()
		child := done("t1")
		g := NewToolGroupMessageItem(sty, child)
		require.True(t, g.ToggleExpanded())

		// The singleton's full view is the call itself: the group bar is
		// the call's own bar, so the render equals the top-level call's
		// render, with no group indent column on top.
		assert.Equal(t, ansi.Strip(child.Render(80)), ansi.Strip(g.Render(80)))
	})

	t.Run("group children sit one indent column inside the bar", func(t *testing.T) {
		t.Parallel()
		child := done("t1")
		g := NewToolGroupMessageItem(sty, child)
		g.AddTool(done("t2"))
		g.ExpandAndDescend()
		g.ToggleSelectedChild()

		// Each nesting level is one indent column narrower than the top
		// level, and each expanded child line is its full body under the
		// group bar plus that one indent column.
		require.Equal(t, 78, ToolBodyWidth(80, 0))
		require.Equal(t, 77, ToolBodyWidth(80, 1))
		bodyLines := strings.Split(ansi.Strip(child.BodyRender(ToolBodyWidth(80, 1))), "\n")
		out := strings.Split(ansi.Strip(g.Render(80)), "\n")
		require.Greater(t, len(out), len(bodyLines)+1)
		for i, ln := range bodyLines {
			assert.Equal(t, "   "+ln, out[i+1], "child line %d", i)
		}
	})

	t.Run("collapsed pair shows one ran row", func(t *testing.T) {
		t.Parallel()
		g := NewToolGroupMessageItem(sty, done("t1"))
		g.AddTool(done("t2"))
		out := ansi.Strip(g.Render(80))
		assert.Contains(t, out, "Ran")
		assert.Contains(t, out, "(2 tool calls)")
		assert.Equal(t, 1, strings.Count(out, "\n")+1)
	})

	t.Run("space toggles the one-liner level", func(t *testing.T) {
		t.Parallel()
		g := NewToolGroupMessageItem(sty, done("t1"))
		g.AddTool(done("t2"))

		// Space opens the one-liner level: one line per call.
		require.True(t, g.ToggleExpanded())
		out := ansi.Strip(g.Render(80))
		assert.Contains(t, out, "(2 tool calls)")
		assert.Equal(t, 2, strings.Count(out, "gh issue close 16"))
		assert.Equal(t, 3, strings.Count(out, "\n")+1)

		// Space again collapses back to the single row.
		require.False(t, g.ToggleExpanded())
		out = ansi.Strip(g.Render(80))
		assert.Contains(t, out, "(2 tool calls)")
		assert.Equal(t, 1, strings.Count(out, "\n")+1)
	})

	t.Run("enter descends and escape climbs back out", func(t *testing.T) {
		t.Parallel()
		g := NewToolGroupMessageItem(sty, done("t1"))
		g.AddTool(done("t2"))

		// Enter opens the group and puts the sub-cursor on the first
		// call; a second enter expands that call's full view.
		g.ExpandAndDescend()
		require.True(t, g.ExpandedLevel())
		require.Equal(t, 0, g.SelectedChild())
		g.ToggleSelectedChild()
		require.True(t, g.ChildTool("t1").ExpansionLevel() != 0)
		out := ansi.Strip(g.Render(80))
		assert.Greater(t, strings.Count(out, "\n")+1, 3, "the expanded call shows its body")

		// Escape: the call collapses with the cursor kept on it, then
		// the group collapses, then escape stops consuming.
		require.True(t, g.Ascend())
		require.False(t, g.ChildTool("t1").ExpansionLevel() != 0)
		require.True(t, g.ExpandedLevel())
		require.True(t, g.Ascend())
		require.False(t, g.ExpandedLevel())
		require.False(t, g.Ascend())
	})

	t.Run("escape on a collapsed call collapses the group", func(t *testing.T) {
		t.Parallel()
		g := NewToolGroupMessageItem(sty, done("t1"))
		g.AddTool(done("t2"))

		// Nothing expanded: escape collapses the run in one press,
		// without an intermediate selection-clearing step.
		g.ExpandAndDescend()
		require.True(t, g.Ascend())
		require.False(t, g.ExpandedLevel())
		require.Equal(t, -1, g.SelectedChild())

		// Even with another call expanded elsewhere, escape on a
		// collapsed call (or the group row) closes the whole run.
		g.ExpandAndDescend()
		g.ToggleSelectedChild()
		require.True(t, g.SelectChildNext())
		require.True(t, g.Ascend())
		require.False(t, g.ExpandedLevel())
		assert.False(t, g.ChildTool("t1").ExpansionLevel() != 0)
		assert.False(t, g.ChildTool("t2").ExpansionLevel() != 0)
	})

	t.Run("collapsed group stays one line while a call runs", func(t *testing.T) {
		t.Parallel()
		g := NewToolGroupMessageItem(sty, done("t1"))
		g.AddTool(bashTool("t2", "npm test", false))
		out := ansi.Strip(g.Render(80))
		assert.Contains(t, out, "Running (2 tool calls)")
		assert.NotContains(t, out, "npm test")
		// The color says the run is in flight.
		assert.Contains(t, g.Render(80), sty.Tool.NamePending.Render("Running"))
	})

	t.Run("all-failed run colors the verb red", func(t *testing.T) {
		t.Parallel()
		item := bashTool("t1", "make build", true)
		item.SetResult(&message.ToolResult{ToolCallID: "t1", Name: "shell", Content: "boom", IsError: true})
		item2 := bashTool("t2", "make test", true)
		item2.SetResult(&message.ToolResult{ToolCallID: "t2", Name: "shell", Content: "boom", IsError: true})
		g := NewToolGroupMessageItem(sty, item)
		g.AddTool(item2)
		out := g.Render(80)
		assert.Contains(t, out, sty.Tool.NameError.Render("Ran"))
		assert.NotContains(t, out, sty.Tool.NamePartial.Render("Ran"))
	})

	t.Run("partial failure colors the verb yellow without red", func(t *testing.T) {
		t.Parallel()
		item := bashTool("t1", "make build", true)
		item.SetResult(&message.ToolResult{ToolCallID: "t1", Name: "shell", Content: "boom", IsError: true})
		g := NewToolGroupMessageItem(sty, item)
		g.AddTool(done("t2"))
		out := g.Render(80)
		assert.Contains(t, out, sty.Tool.NamePartial.Render("Ran"))
		assert.NotContains(t, out, sty.Tool.NameError.Render("Ran"))
	})

	t.Run("all-succeeded run colors the verb blue", func(t *testing.T) {
		t.Parallel()
		g := NewToolGroupMessageItem(sty, done("t1"))
		g.AddTool(done("t2"))
		out := g.Render(80)
		assert.Contains(t, out, sty.Tool.NameNormal.Render("Ran"))
		assert.NotContains(t, out, sty.Tool.NameError.Render("Ran"))
	})

	t.Run("one-liners prettify the tool name", func(t *testing.T) {
		t.Parallel()
		// Tool calls arrive with the raw lowercase name; the one-liner
		// must label them the way the full renderers do, matching the
		// capitalized group verb ("Ran").
		item := NewToolMessageItem(sty, "msg", message.ToolCall{
			ID: "t1", Name: "edit", Input: `{"file_path":"/a/b.go"}`, Finished: true,
		}, nil, false)
		g := NewToolGroupMessageItem(sty, item)
		out := ansi.Strip(g.Render(80))
		assert.Contains(t, out, "Edit")
		assert.NotContains(t, out, "edit")
	})
}

// TestSelectChildFromBelow pins the group's from-below landing: a
// selection arriving from the item below parks the sub-cursor on the
// bottommost call while the one-liner level is open, and on the group
// row for a collapsed run or a singleton whose only line is the call
// itself.
func TestSelectChildFromBelow(t *testing.T) {
	t.Parallel()
	sty := groupStyles()

	done := func(id string) ToolMessageItem {
		item := bashTool(id, "ls", true)
		item.SetResult(&message.ToolResult{ToolCallID: id, Name: "shell", Content: "ok"})
		return item
	}

	t.Run("expanded run takes the bottommost call", func(t *testing.T) {
		t.Parallel()
		g := NewToolGroupMessageItem(sty, done("t1"))
		g.AddTool(done("t2"))
		g.AddTool(done("t3"))
		require.True(t, g.ToggleExpanded())

		g.SelectChildFromBelow()
		require.Equal(t, 2, g.SelectedChild(), "the bottommost call wins, not the group row")
	})

	t.Run("collapsed run keeps the group row", func(t *testing.T) {
		t.Parallel()
		g := NewToolGroupMessageItem(sty, done("t1"))
		g.AddTool(done("t2"))

		g.SelectChildFromBelow()
		require.Equal(t, -1, g.SelectedChild())
	})

	t.Run("expanded singleton keeps the group row", func(t *testing.T) {
		t.Parallel()
		g := NewToolGroupMessageItem(sty, done("t1"))
		require.True(t, g.ToggleExpanded())

		g.SelectChildFromBelow()
		require.Equal(t, -1, g.SelectedChild(), "a singleton's only line is the call itself")
	})
}

// TestSelectionColorsToolNames pins the two-tone rule the transcript
// lives by: tool names stay in the understated grey until the entry is
// selected or expanded, then say their status in color (blue done,
// green pending; failures were never grey).
func TestSelectionColorsToolNames(t *testing.T) {
	t.Parallel()
	sty := groupStyles()
	done := func(id string) ToolMessageItem {
		item := bashTool(id, "go test", true)
		item.SetResult(&message.ToolResult{ToolCallID: id, Name: "shell", Content: "ok"})
		return item
	}

	t.Run("collapsed group colors the verb only while selected", func(t *testing.T) {
		t.Parallel()
		g := NewToolGroupMessageItem(sty, done("t1"))
		g.AddTool(done("t2"))
		require.Contains(t, g.Render(80), sty.Tool.NameNormal.Render("Ran"))
		require.NotContains(t, g.Render(80), sty.Tool.NameNormalSelected.Render("Ran"))

		g.SetFocused(true)
		require.Contains(t, g.Render(80), sty.Tool.NameNormalSelected.Render("Ran"))
	})

	t.Run("selected singleton one-liner colors the name", func(t *testing.T) {
		t.Parallel()
		g := NewToolGroupMessageItem(sty, done("t1"))
		require.Contains(t, g.Render(80), sty.Tool.NameNormal.Render("Shell"))

		g.SetFocused(true)
		require.Contains(t, g.Render(80), sty.Tool.NameNormalSelected.Render("Shell"))
	})

	t.Run("sub-cursor colors its child and returns the verb to grey", func(t *testing.T) {
		t.Parallel()
		g := NewToolGroupMessageItem(sty, done("t1"))
		g.AddTool(done("t2"))
		g.SetFocused(true)
		g.ExpandAndDescend()
		require.Equal(t, 0, g.SelectedChild())

		out := g.Render(120)
		assert.Contains(t, out, sty.Tool.NameNormalSelected.Render("Shell"))
		assert.Contains(t, out, sty.Tool.NameNormal.Render("Shell"))
		assert.NotContains(t, out, sty.Tool.NameNormalSelected.Render("Ran"))
	})

	t.Run("expanded call colors its header name regardless of focus", func(t *testing.T) {
		t.Parallel()
		g := NewToolGroupMessageItem(sty, done("t1"))
		g.AddTool(done("t2"))
		g.ExpandAndDescend()
		g.DigIn()
		require.True(t, g.ChildTool("t1").ExpansionLevel() != 0)

		assert.Contains(t, g.Render(120), sty.Tool.NameNormalSelected.Render("Shell"))
	})

	t.Run("selected live run says pending in color", func(t *testing.T) {
		t.Parallel()
		g := NewToolGroupMessageItem(sty, done("t1"))
		g.AddTool(bashTool("t2", "npm test", false))
		require.Contains(t, g.Render(80), sty.Tool.NamePending.Render("Running"))

		g.SetFocused(true)
		assert.Contains(t, g.Render(80), sty.Tool.NamePendingSelected.Render("Running"))
	})
}

func TestToolGroupChildResolution(t *testing.T) {
	t.Parallel()
	sty := groupStyles()
	g := NewToolGroupMessageItem(sty, bashTool("t1", "ls", true))
	g.AddTool(bashTool("t2", "pwd", true))

	assert.NotNil(t, g.ChildTool("t2"))
	assert.Nil(t, g.ChildTool("missing"))
	assert.False(t, g.Finished(), "running children keep the group unfrozen")
}

// TestToolGroupAdvanceBumpsVersion is the spinner regression test for
// groups: clock frames must advance the group spinner and every
// spinning child and bump the group on every glyph change, because the
// list only checks the group's version — children are not list entries
// of their own. A settled group must not bump.
func TestToolGroupAdvanceBumpsVersion(t *testing.T) {
	t.Parallel()
	sty := groupStyles()

	g := NewToolGroupMessageItem(sty, bashTool("t1", "ls", true))
	require.True(t, g.Spinning())
	before := g.Version()
	bumped := false
	for range pulseTestFrames {
		if g.Advance() {
			bumped = true
		}
	}
	require.True(t, bumped, "a spinning group must request re-renders on glyph changes")
	require.Greater(t, g.Version(), before, "a spinning group must bump so the list cache re-renders")

	// Settling every child freezes the group: no more frames, no bumps.
	g.ChildTool("t1").SetResult(&message.ToolResult{ToolCallID: "t1", Name: "shell", Content: "ok"})
	g.clearCache()
	require.False(t, g.Spinning())
	settled := g.Version()
	require.False(t, g.Advance())
	require.Equal(t, settled, g.Version(), "a settled group must not bump")
}

// TestCollapsedGroupHidesItsChildren pins the collapsed group to a
// single header line, spinning children or not: what the calls did
// waits for an expansion, and the header's color alone says the run is
// still in flight.
func TestCollapsedGroupHidesItsChildren(t *testing.T) {
	t.Parallel()

	g := NewToolGroupMessageItem(groupStyles(), bashTool("a1", "first", false))
	g.AddTool(bashTool("a2", "second", true))
	g.AddTool(bashTool("a3", "third", false))
	require.True(t, g.Spinning(), "the unfinished call keeps the group live")

	lines, selStart, selEnd := g.renderLines(120)
	require.Len(t, lines, 1, "a collapsed group renders only its header")
	require.Equal(t, -1, selStart)
	require.Equal(t, -1, selEnd)
	stripped := ansi.Strip(lines[0])
	require.Contains(t, stripped, "Running (3 tool calls)")
	require.NotContains(t, stripped, "first")
	require.NotContains(t, stripped, "third")
}
