package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
	"github.com/stubbedev/harness/internal/config"
)

// fakeTool records the inputs it is called with and answers with
// whatever respond returns.
type fakeTool struct {
	name    string
	respond func(input map[string]any) (fantasy.ToolResponse, error)

	mu    sync.Mutex
	calls []map[string]any
	// peak tracks the highest number of concurrently in-flight calls so
	// the fan-out tests can assert parallelism actually happens.
	inFlight atomic.Int32
	peak     atomic.Int32
}

func (f *fakeTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{Name: f.name, Description: f.name}
}
func (f *fakeTool) ProviderOptions() fantasy.ProviderOptions        { return nil }
func (f *fakeTool) SetProviderOptions(opts fantasy.ProviderOptions) {}

func (f *fakeTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	now := f.inFlight.Add(1)
	for {
		peak := f.peak.Load()
		if now <= peak || f.peak.CompareAndSwap(peak, now) {
			break
		}
	}
	defer f.inFlight.Add(-1)

	var input map[string]any
	if err := json.Unmarshal([]byte(call.Input), &input); err != nil {
		return fantasy.ToolResponse{}, err
	}
	f.mu.Lock()
	f.calls = append(f.calls, input)
	f.mu.Unlock()
	return f.respond(input)
}

func (f *fakeTool) callInputs() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// jsonTool answers with the JSON encoding of whatever build returns.
func jsonTool(name string, build func(input map[string]any) any) *fakeTool {
	return &fakeTool{
		name: name,
		respond: func(input map[string]any) (fantasy.ToolResponse, error) {
			out, err := json.Marshal(build(input))
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			return fantasy.NewTextResponse(string(out)), nil
		},
	}
}

func runPlan(t *testing.T, params BatchParams, siblings ...fantasy.AgentTool) fantasy.ToolResponse {
	t.Helper()
	resp, err := runBatch(t.Context(), params, siblings)
	require.NoError(t, err)
	return resp
}

// decodeResult parses a successful batch response back into a value.
func decodeResult(t *testing.T, resp fantasy.ToolResponse) any {
	t.Helper()
	require.False(t, resp.IsError, "unexpected error response: %s", resp.Content)
	var v any
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &v))
	return v
}

func TestBatch_SingleStepReturnsToolOutput(t *testing.T) {
	t.Parallel()

	tool := jsonTool("lookup", func(input map[string]any) any {
		return map[string]any{"id": input["id"], "title": "hello"}
	})

	resp := runPlan(t, BatchParams{
		Steps: []BatchStep{{ID: "one", Tool: "lookup", Input: map[string]any{"id": 7.0}}},
	}, tool)

	require.Equal(t, map[string]any{"id": 7.0, "title": "hello"}, decodeResult(t, resp))
}

// The whole point of the tool: many calls, one small result.
func TestBatch_ForEachFansOutAndReturnFilters(t *testing.T) {
	t.Parallel()

	list := jsonTool("list", func(map[string]any) any {
		return []any{
			map[string]any{"id": 1.0},
			map[string]any{"id": 2.0},
			map[string]any{"id": 3.0},
		}
	})
	get := jsonTool("get", func(input map[string]any) any {
		id := input["id"].(float64)
		// Only the middle record is missing a title.
		title := fmt.Sprintf("post %d", int(id))
		if id == 2 {
			title = ""
		}
		return map[string]any{"id": id, "title": title}
	})

	resp := runPlan(t, BatchParams{
		Steps: []BatchStep{
			{ID: "hits", Tool: "list"},
			{
				ID:      "full",
				Tool:    "get",
				ForEach: "$hits | map(.id)",
				InputJQ: "{id: $item}",
			},
		},
		Return: `$full | map(select(.title == "")) | map(.id)`,
	}, list, get)

	require.Equal(t, []any{2.0}, decodeResult(t, resp))
	require.Len(t, get.callInputs(), 3, "every item must be visited")
}

func TestBatch_ForEachIteratesStreamAndArrayAlike(t *testing.T) {
	t.Parallel()

	for _, expr := range []string{"$src", "$src | .[]", "$src | map(. * 2) | map(. / 2)"} {
		t.Run(expr, func(t *testing.T) {
			t.Parallel()
			src := jsonTool("src", func(map[string]any) any { return []any{1.0, 2.0} })
			echo := jsonTool("echo", func(input map[string]any) any {
				return map[string]any{"v": input["v"]}
			})

			resp := runPlan(t, BatchParams{
				Steps: []BatchStep{
					{ID: "src", Tool: "src"},
					{ID: "out", Tool: "echo", ForEach: expr, InputJQ: "{v: $item}"},
				},
				Return: "$out | map(.v)",
			}, src, echo)

			require.Equal(t, []any{1.0, 2.0}, decodeResult(t, resp))
		})
	}
}

func TestBatch_ForEachBindsIndex(t *testing.T) {
	t.Parallel()

	echo := jsonTool("echo", func(input map[string]any) any { return input })
	resp := runPlan(t, BatchParams{
		Steps: []BatchStep{
			{ID: "out", Tool: "echo", ForEach: `["a","b"]`, InputJQ: "{v: $item, i: $index}"},
		},
		Return: "$out | map(.i)",
	}, echo)

	require.Equal(t, []any{0.0, 1.0}, decodeResult(t, resp))
}

// Results must line up with the input items even though the calls race.
func TestBatch_ForEachPreservesOrderUnderParallelism(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	echo := &fakeTool{
		name: "echo",
		respond: func(input map[string]any) (fantasy.ToolResponse, error) {
			// Every call blocks until the last one arrives, so the
			// completion order cannot match the input order.
			<-release
			out, _ := json.Marshal(map[string]any{"v": input["v"]})
			return fantasy.NewTextResponse(string(out)), nil
		},
	}
	go func() {
		for echo.inFlight.Load() < batchParallelism {
		}
		close(release)
	}()

	resp := runPlan(t, BatchParams{
		Steps: []BatchStep{
			{ID: "out", Tool: "echo", ForEach: "[0,1,2,3,4,5,6,7]", InputJQ: "{v: $item}"},
		},
		Return: "$out | map(.v)",
	}, echo)

	require.Equal(t, []any{0.0, 1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0}, decodeResult(t, resp))
	require.Greater(t, int(echo.peak.Load()), 1, "fan-out must actually run in parallel")
	require.LessOrEqual(t, int(echo.peak.Load()), batchParallelism, "parallelism must stay bounded")
}

func TestBatch_InputJQMergesOverLiteralInput(t *testing.T) {
	t.Parallel()

	echo := jsonTool("echo", func(input map[string]any) any { return input })
	resp := runPlan(t, BatchParams{
		Steps: []BatchStep{{
			ID:      "one",
			Tool:    "echo",
			Input:   map[string]any{"keep": "yes", "override": "before"},
			InputJQ: `{override: "after"}`,
		}},
	}, echo)

	require.Equal(t, map[string]any{"keep": "yes", "override": "after"}, decodeResult(t, resp))
}

func TestBatch_WhenSkipsStep(t *testing.T) {
	t.Parallel()

	echo := jsonTool("echo", func(map[string]any) any { return map[string]any{"did": "run"} })
	resp := runPlan(t, BatchParams{
		Steps: []BatchStep{
			{ID: "skipped", Tool: "echo", When: "false"},
			{ID: "ran", Tool: "echo", When: "true"},
		},
		Return: "{skipped: $skipped, ran: $ran.did}",
	}, echo)

	require.Equal(t, map[string]any{"skipped": nil, "ran": "run"}, decodeResult(t, resp))
	require.Len(t, echo.callInputs(), 1, "the gated step must not call its tool")
}

func TestBatch_ErrorAbortsByDefault(t *testing.T) {
	t.Parallel()

	boom := &fakeTool{
		name: "boom",
		respond: func(map[string]any) (fantasy.ToolResponse, error) {
			return fantasy.NewTextErrorResponse("kaboom"), nil
		},
	}
	after := jsonTool("after", func(map[string]any) any { return "reached" })

	resp := runPlan(t, BatchParams{
		Steps: []BatchStep{
			{ID: "bad", Tool: "boom"},
			{ID: "next", Tool: "after"},
		},
	}, boom, after)

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "kaboom")
	require.Contains(t, resp.Content, `step "bad"`)
	require.Empty(t, after.callInputs(), "a failed step must stop the plan")
}

func TestBatch_OnErrorCollectKeepsGoing(t *testing.T) {
	t.Parallel()

	flaky := &fakeTool{
		name: "flaky",
		respond: func(input map[string]any) (fantasy.ToolResponse, error) {
			if input["v"].(float64) == 2 {
				return fantasy.NewTextErrorResponse("item two is bad"), nil
			}
			return fantasy.NewTextResponse(`{"status":"ok"}`), nil
		},
	}

	resp := runPlan(t, BatchParams{
		Steps: []BatchStep{{
			ID:      "out",
			Tool:    "flaky",
			ForEach: "[1,2,3]",
			InputJQ: "{v: $item}",
			OnError: onErrorCollect,
		}},
		Return: "$out",
	}, flaky)

	require.Equal(t, []any{
		map[string]any{"status": "ok"},
		map[string]any{"error": "item two is bad"},
		map[string]any{"status": "ok"},
	}, decodeResult(t, resp))
}

func TestBatch_MetadataCountsCallsAndErrors(t *testing.T) {
	t.Parallel()

	flaky := &fakeTool{
		name: "flaky",
		respond: func(input map[string]any) (fantasy.ToolResponse, error) {
			if input["v"].(float64) == 2 {
				return fantasy.NewTextErrorResponse("bad"), nil
			}
			return fantasy.NewTextResponse(`{"status":"ok"}`), nil
		},
	}

	resp := runPlan(t, BatchParams{
		Steps: []BatchStep{{
			ID: "out", Tool: "flaky", ForEach: "[1,2,3]",
			InputJQ: "{v: $item}", OnError: onErrorCollect,
		}},
		Return: "0",
	}, flaky)

	var meta BatchResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))

	require.Equal(t, 1, meta.Steps)
	require.Equal(t, 3, meta.ToolCalls)
	require.Equal(t, 1, meta.Errors)
	require.Equal(t, map[string]int{"out": 3}, meta.PerStep)
}

func TestBatch_NonJSONToolOutputStaysAString(t *testing.T) {
	t.Parallel()

	text := &fakeTool{
		name: "text",
		respond: func(map[string]any) (fantasy.ToolResponse, error) {
			return fantasy.NewTextResponse("just some prose"), nil
		},
	}

	resp := runPlan(t, BatchParams{
		Steps:  []BatchStep{{ID: "one", Tool: "text"}},
		Return: "$one | test(\"prose\")",
	}, text)

	require.Equal(t, true, decodeResult(t, resp))
}

func TestBatch_ReturnDefaultsToLastStep(t *testing.T) {
	t.Parallel()

	first := jsonTool("first", func(map[string]any) any { return map[string]any{"n": "a"} })
	second := jsonTool("second", func(map[string]any) any { return map[string]any{"n": "b"} })

	resp := runPlan(t, BatchParams{
		Steps: []BatchStep{
			{ID: "one", Tool: "first"},
			{ID: "two", Tool: "second"},
		},
	}, first, second)

	require.Equal(t, map[string]any{"n": "b"}, decodeResult(t, resp))
}

func TestBatch_Rejects(t *testing.T) {
	t.Parallel()

	echo := jsonTool("echo", func(map[string]any) any { return map[string]any{"n": 1.0} })

	tests := []struct {
		name   string
		params BatchParams
		want   string
	}{
		{
			name:   "no steps",
			params: BatchParams{},
			want:   "steps is required",
		},
		{
			name:   "unknown tool",
			params: BatchParams{Steps: []BatchStep{{ID: "a", Tool: "nope"}}},
			want:   `unknown tool "nope"`,
		},
		{
			name: "duplicate id",
			params: BatchParams{Steps: []BatchStep{
				{ID: "a", Tool: "echo"},
				{ID: "a", Tool: "echo"},
			}},
			want: "duplicate id",
		},
		{
			name:   "missing id",
			params: BatchParams{Steps: []BatchStep{{Tool: "echo"}}},
			want:   "id is required",
		},
		{
			name:   "id is not a jq identifier",
			params: BatchParams{Steps: []BatchStep{{ID: "has-dash", Tool: "echo"}}},
			want:   "jq-safe identifier",
		},
		{
			name:   "bad jq in return",
			params: BatchParams{Steps: []BatchStep{{ID: "a", Tool: "echo"}}, Return: "$a |"},
			want:   "return:",
		},
		{
			name: "input_jq that is not an object",
			params: BatchParams{Steps: []BatchStep{
				{ID: "a", Tool: "echo", InputJQ: "42"},
			}},
			want: "must produce an object",
		},
		{
			name: "reference to a later step",
			params: BatchParams{Steps: []BatchStep{
				{ID: "a", Tool: "echo", InputJQ: "{v: $b}"},
				{ID: "b", Tool: "echo"},
			}},
			want: "variable not defined: $b",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resp := runPlan(t, tc.params, echo)
			require.True(t, resp.IsError, "expected an error response, got: %s", resp.Content)
			require.Contains(t, resp.Content, tc.want)
		})
	}
}

func TestBatch_RejectsTooManySteps(t *testing.T) {
	t.Parallel()

	echo := jsonTool("echo", func(map[string]any) any { return map[string]any{"n": 1.0} })
	steps := make([]BatchStep, maxBatchSteps+1)
	for i := range steps {
		steps[i] = BatchStep{ID: fmt.Sprintf("s%d", i), Tool: "echo"}
	}

	resp := runPlan(t, BatchParams{Steps: steps}, echo)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "too many steps")
}

func TestBatch_RejectsOversizedFanOut(t *testing.T) {
	t.Parallel()

	echo := jsonTool("echo", func(map[string]any) any { return map[string]any{"n": 1.0} })
	resp := runPlan(t, BatchParams{
		Steps: []BatchStep{{
			ID: "out", Tool: "echo",
			ForEach: fmt.Sprintf("[range(%d)]", maxFanOut+1),
			InputJQ: "{v: $item}",
		}},
	}, echo)

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "max 256")
	require.Empty(t, echo.callInputs(), "an oversized fan-out must not call the tool at all")
}

// Returning everything defeats the purpose, so it is refused rather than
// silently flooding the conversation.
func TestBatch_RejectsOversizedResult(t *testing.T) {
	t.Parallel()

	big := jsonTool("big", func(map[string]any) any {
		return map[string]any{"blob": strings.Repeat("x", 1024)}
	})

	resp := runPlan(t, BatchParams{
		Steps: []BatchStep{{
			ID: "out", Tool: "big",
			ForEach: "[range(100)]",
			InputJQ: "{v: $item}",
		}},
		Return: "$out",
	}, big)

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "over the")
	require.Contains(t, resp.Content, "Narrow the 'return' expression")
}

func TestBatch_EmptyForEachRunsNothing(t *testing.T) {
	t.Parallel()

	echo := jsonTool("echo", func(map[string]any) any { return map[string]any{"n": 1.0} })
	resp := runPlan(t, BatchParams{
		Steps:  []BatchStep{{ID: "out", Tool: "echo", ForEach: "[]", InputJQ: "{v: $item}"}},
		Return: "$out",
	}, echo)

	require.Equal(t, []any{}, decodeResult(t, resp))
	require.Empty(t, echo.callInputs())
}

// A plan must not be able to nest another plan: the coordinator builds the
// tool from a list that excludes it.
func TestBatch_CannotCallItself(t *testing.T) {
	t.Parallel()

	echo := jsonTool("echo", func(map[string]any) any { return map[string]any{"n": 1.0} })
	callable := []fantasy.AgentTool{echo}
	batch := NewBatchTool(func() []fantasy.AgentTool { return callable })

	input, err := json.Marshal(BatchParams{
		Steps: []BatchStep{{ID: "a", Tool: BatchToolName}},
	})
	require.NoError(t, err)

	resp, err := batch.Run(t.Context(), fantasy.ToolCall{Input: string(input)})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, `unknown tool "batch"`)
}

func TestBatch_RejectsUnknownOnError(t *testing.T) {
	t.Parallel()

	echo := jsonTool("echo", func(map[string]any) any { return map[string]any{"n": 1.0} })
	resp := runPlan(t, BatchParams{
		Steps: []BatchStep{{ID: "a", Tool: "echo", OnError: "Collect"}},
	}, echo)

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "on_error must be")
	require.Empty(t, echo.callInputs(), "a malformed step must not call its tool")
}

// TestBatch_ComposesRealTools runs a plan over the actual glob and view
// tools. The fakes above pin the semantics; this pins that the tool works
// against the shapes real tools actually return.
func TestBatch_ComposesRealTools(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha\nTODO: one\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("beta\nnothing here\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "c.txt"), []byte("gamma\nTODO: two\n"), 0o644))

	ctx := context.WithValue(t.Context(), SessionIDContextKey, "batch-session")
	resp, err := runBatch(ctx, BatchParams{
		Steps: []BatchStep{
			{ID: "files", Tool: ShellToolName, Input: map[string]any{"command": "ls -1 *.txt"}},
			{
				ID:      "bodies",
				Tool:    ViewToolName,
				ForEach: `$files | split("\n") | map(select(endswith(".txt")))`,
				InputJQ: "{file_path: $item}",
			},
		},
		// Count the files whose body mentions TODO without ever putting
		// the bodies themselves into the result.
		Return: `[$bodies[] | select(test("TODO"))] | length`,
	}, []fantasy.AgentTool{
		NewBashTool(dir, "batch", &config.Attribution{}, "test-model", nil),
		NewViewTool(nil, mockFileTracker{}, nil, dir),
	})

	require.NoError(t, err)
	require.False(t, resp.IsError, "unexpected error: %s", resp.Content)
	require.Equal(t, "2", strings.TrimSpace(resp.Content))
}

// A tool that owns one shared session must not be fanned out into
// itself: bash has a single terminal per working directory, so
// concurrent calls interleave rather than parallelise.
func TestBatch_ForEachRunsSerialToolsOneAtATime(t *testing.T) {
	t.Parallel()

	require.True(t, serialTools[ShellToolName], "bash must be registered as serial")

	seen := jsonTool(ShellToolName, func(input map[string]any) any {
		return map[string]any{"v": input["v"]}
	})

	resp := runPlan(t, BatchParams{
		Steps: []BatchStep{
			{ID: "out", Tool: ShellToolName, ForEach: "[0,1,2,3,4,5,6,7]", InputJQ: "{v: $item}"},
		},
		Return: "$out | map(.v)",
	}, seen)

	require.Equal(t, []any{0.0, 1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0}, decodeResult(t, resp))
	require.Equal(t, 1, int(seen.peak.Load()), "a serial tool must never run alongside itself")
}
