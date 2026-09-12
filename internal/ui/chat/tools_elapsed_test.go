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
	tc := message.ToolCall{ID: "toolu_1", Name: "bash", Finished: false}
	item := newBaseToolMessageItem(&sty, tc, nil, &BashToolRenderContext{}, false)

	// A live tool's timer runs from the moment the item was built. How
	// soon that shows as more than zero is the clock's business, not the
	// item's: Windows advances its monotonic clock about once a
	// millisecond, so reading the timer immediately after construction
	// legitimately measures nothing at all.
	require.Eventually(t, func() bool {
		return item.elapsed() > 0
	}, time.Second, time.Millisecond, "a live tool must report elapsed time")

	item.SetToolCall(message.ToolCall{ID: "toolu_1", Name: "bash", Finished: true})
	require.False(t, item.finishedAt.IsZero(), "finishing must capture the end time")

	frozen := item.elapsed()
	time.Sleep(10 * time.Millisecond)
	require.Equal(t, frozen, item.elapsed(), "elapsed must stop growing once finished")

	item.markRestored()
	require.Zero(t, item.elapsed(), "restored items have no trustworthy start time")
}
