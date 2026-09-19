package agent

import (
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/subagents"
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
	require.Equal(t, subagents.StatusCompleted, state.Jobs[0].Status)
}
