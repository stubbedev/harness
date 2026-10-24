package chat

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/ui/styles"
)

func TestBaseToolMessageItemElapsed(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	tc := message.ToolCall{ID: "toolu_1", Name: "shell", Finished: false}
	item := newBaseToolMessageItem(&sty, tc, nil, &ShellToolRenderContext{}, false, nil)

	// A live tool's timer runs from the moment the item was built. How
	// soon that shows as more than zero is the clock's business, not the
	// item's: Windows advances its monotonic clock about once a
	// millisecond, so reading the timer immediately after construction
	// legitimately measures nothing at all.
	require.Eventually(t, func() bool {
		return item.Elapsed() > 0
	}, time.Second, time.Millisecond, "a live tool must report elapsed time")

	item.SetToolCall(message.ToolCall{ID: "toolu_1", Name: "shell", Finished: true})
	require.False(t, item.finishedAt.IsZero(), "finishing must capture the end time")

	frozen := item.Elapsed()
	time.Sleep(10 * time.Millisecond)
	require.Equal(t, frozen, item.Elapsed(), "elapsed must stop growing once finished")

	item.markRestored()
	require.Zero(t, item.Elapsed(), "restored items have no trustworthy start time")
}

// TestResultArrivalFreezesElapsed pins the settle path the transcript
// uses when a result lands without an observed finish transition: the
// duration shown for the call must stop growing, or a cached render
// would keep displaying a timer that never was.
func TestResultArrivalFreezesElapsed(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	item := newBaseToolMessageItem(&sty, message.ToolCall{ID: "toolu_1", Name: "shell"}, nil, &ShellToolRenderContext{}, false, nil)
	item.startedAt = time.Now().Add(-3 * time.Second)

	item.SetResult(&message.ToolResult{Content: "ok"})
	require.False(t, item.finishedAt.IsZero(), "a result must freeze the elapsed time")
	frozen := item.Elapsed()
	time.Sleep(10 * time.Millisecond)
	require.Equal(t, frozen, item.Elapsed(), "elapsed must stop growing once the result lands")
}

// TestCanceledItemHasNoDuration pins that a call canceled before it ran
// shows no duration, instead of a timer that keeps counting.
func TestCanceledItemHasNoDuration(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	item := newBaseToolMessageItem(&sty, message.ToolCall{ID: "toolu_1", Name: "shell"}, nil, &ShellToolRenderContext{}, true, nil)
	require.Zero(t, item.Elapsed())
}
