package agent

import (
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/subagents"
	"github.com/stubbedev/harness/internal/toolname"
)

func TestBackgroundMetadataSurvivesCompactionAndUpdates(t *testing.T) {
	t.Parallel()
	run := &backgroundRun{handle: "bg-one", childSession: "child", agentName: "fast"}
	state := newExecutionState("")
	response := withBackgroundMetadata(fantasy.NewTextResponse("started"), []*backgroundRun{run})
	state.Ingest(executionMessages("dispatch", "agent", `{"prompt":"inspect"}`, response.Metadata, false))
	require.Len(t, state.Jobs, 1)
	require.Equal(t, "running", state.Jobs[0].Status)
	state = newExecutionState(state.Summary("narrative"))
	run.finish(subagents.StatusCompleted, fantasy.NewTextResponse("done"))
	response = withBackgroundMetadata(fantasy.NewTextResponse("collected"), []*backgroundRun{run})
	state.Ingest(executionMessages("wait", "agent", `{"handles":["bg-one"]}`, response.Metadata, false))
	require.Len(t, state.Jobs, 1)
	require.Equal(t, string(subagents.StatusCompleted), state.Jobs[0].Status)
}

// TestWaitToolMetadataUpdatesTheTaskStrip: the wait tool hands back the
// same jobs metadata a dispatch does, so an agent's strip row learns it
// finished from the wait that collected it, not only from the agent tool.
func TestWaitToolMetadataUpdatesTheTaskStrip(t *testing.T) {
	t.Parallel()

	run := &backgroundRun{handle: "bg-one", childSession: "child", agentName: "fast"}
	state := newExecutionState("")
	state.Ingest(executionMessages("dispatch", "agent", `{"prompt":"inspect"}`,
		withBackgroundMetadata(fantasy.NewTextResponse("started"), []*backgroundRun{run}).Metadata, false))

	run.finish(subagents.StatusCompleted, fantasy.NewTextResponse("done"))
	collected := withBackgroundMetadata(fantasy.NewTextResponse("collected"), []*backgroundRun{run})
	state.Ingest(executionMessages("wait", toolname.Wait, `{"wait_for":["bg-one"]}`, collected.Metadata, false))

	require.Len(t, state.Jobs, 1)
	require.Equal(t, string(subagents.StatusCompleted), state.Jobs[0].Status)
}
