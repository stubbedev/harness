package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/agent"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/styles"
)

// countingToolRenderer stands in for a real tool renderer and counts
// RenderTool calls, so tests can tell a cached body from a re-render.
// While a call is spinning the waiting line must stay out of its
// output (the item appends it per tick); a settled call carries the
// full view.
type countingToolRenderer struct {
	renders int
}

func (c *countingToolRenderer) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	c.renders++
	header := toolHeader(sty, opts.Name, width, opts, "param")
	if opts.Compact {
		return header
	}
	if opts.OmitWaitingLine {
		return header
	}
	return joinToolParts(header, "body")
}

func newCountingTool(t *testing.T) (*baseToolMessageItem, *countingToolRenderer) {
	t.Helper()
	sty := styles.CharmtonePantera()
	tc := message.ToolCall{ID: "toolu_1", Name: "shell", Input: `{"command":"go build"}`}
	r := &countingToolRenderer{}
	return newBaseToolMessageItem(&sty, tc, nil, r, false, nil), r
}

// tick simulates one animation tick: the anim's pulse only advances
// the item every other frame, so Advance is retried until it reports a
// bump.
func tick(t *testing.T, item *baseToolMessageItem) {
	t.Helper()
	for range 4 {
		if item.Advance() {
			return
		}
	}
	t.Fatal("anim never advanced")
}

// TestSpinningToolBodyRenderedOncePerTick pins the spinner hot path: a
// running call's body renders once and is then served from the cache
// across animation ticks, while the elapsed waiting line is re-attached
// fresh per tick.
func TestSpinningToolBodyRenderedOncePerTick(t *testing.T) {
	t.Parallel()

	item, r := newCountingTool(t)

	first := item.Render(80)
	require.Equal(t, 1, r.renders, "first render must run the renderer")
	require.Contains(t, ansi.Strip(first), "Waiting for tool response")

	// Animation ticks must not re-run the renderer: the body is
	// cached and only the waiting suffix is rebuilt.
	tick(t, item)
	second := item.Render(80)
	require.Equal(t, 1, r.renders, "an animation tick must not re-render the body")
	require.Equal(t, first, second)

	// The suffix itself must track the clock: rewinding the start
	// time changes only the waiting line, still without a body
	// re-render.
	item.startedAt = time.Now().Add(-9 * time.Second)
	tick(t, item)
	third := item.Render(80)
	require.Equal(t, 1, r.renders, "the waiting line must update without re-rendering the body")
	require.Contains(t, ansi.Strip(third), "Waiting for tool response for 9s")
	require.NotEqual(t, second, third)

	// A content change (the result landing) invalidates the body.
	res := &message.ToolResult{Content: "done"}
	item.SetResult(res)
	settled := item.Render(80)
	require.Equal(t, 2, r.renders, "a content change must re-render the body")
	require.NotContains(t, ansi.Strip(settled), "Waiting for tool response")
	require.Contains(t, ansi.Strip(settled), "body")
}

// TestSpinningToolRenderMatchesUnsplitRender pins pixel identity: the
// per-tick Render path (cached prefixed body + re-attached suffix) must
// equal the prefix applied line-by-line to the full unsplit view, on
// both the cache-miss and cache-hit paths.
func TestSpinningToolRenderMatchesUnsplitRender(t *testing.T) {
	t.Parallel()

	item, _ := newCountingTool(t)

	prefixLines := func(rendered string) string {
		lines := strings.Split(rendered, "\n")
		for i, ln := range lines {
			lines[i] = item.focusPrefix() + ln
		}
		return strings.Join(lines, "\n")
	}

	miss := item.Render(80)
	require.Equal(t, prefixLines(item.BodyRender(80)), miss, "the miss path must match the unsplit render")

	tick(t, item)
	hit := item.Render(80)
	require.Equal(t, prefixLines(item.BodyRender(80)), hit, "the hit path must match the unsplit render")
	require.Equal(t, miss, hit)
}

// TestSpinningWaitToolKeepsLiveLabelAndNoWaitingLine pins the wait
// tool's running view: no "Waiting for tool response" line, and a
// header label that tracks the live subagent count without re-running
// the renderer per tick.
func TestSpinningWaitToolKeepsLiveLabelAndNoWaitingLine(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	waiting := 2
	env := &ItemEnv{WaitingAgents: func() int { return waiting }}
	tc := message.ToolCall{ID: "toolu_1", Name: agent.AgentToolName, Input: `{}`}
	item := newBaseToolMessageItem(&sty, tc, nil, &WaitToolRenderContext{}, false, env)

	plain := ansi.Strip(item.Render(80))
	require.Contains(t, plain, "Waiting for 2 agents")
	require.NotContains(t, plain, "Waiting for tool response")

	// An agent finishing changes the label; the body re-renders
	// exactly once for it.
	waiting = 1
	tick(t, item)
	updated := ansi.Strip(item.Render(80))
	require.Contains(t, updated, "Waiting for 1 agent")
	require.NotContains(t, updated, "Waiting for 2 agents")

	// Ticks without a count change keep the body cached.
	stable := item.Render(80)
	tick(t, item)
	require.Equal(t, stable, item.Render(80))
}
