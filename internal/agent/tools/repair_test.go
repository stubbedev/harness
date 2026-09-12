package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

// schemaTool is a tool that exists only to carry a schema: the repair
// path reads Info and never runs anything.
type schemaTool struct {
	info fantasy.ToolInfo
}

func (s *schemaTool) Info() fantasy.ToolInfo                     { return s.info }
func (s *schemaTool) ProviderOptions() fantasy.ProviderOptions   { return nil }
func (s *schemaTool) SetProviderOptions(fantasy.ProviderOptions) {}
func (s *schemaTool) Run(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return fantasy.ToolResponse{}, nil
}

func repairFor(t *testing.T, input string, tool fantasy.AgentTool) (*fantasy.ToolCallContent, error) {
	t.Helper()
	return RepairToolCall(t.Context(), fantasy.ToolCallRepairOptions{
		OriginalToolCall: fantasy.ToolCallContent{
			ToolCallID: "call_1",
			ToolName:   tool.Info().Name,
			Input:      input,
		},
		ValidationError: errors.New(validationErrPrefix + "description"),
		AvailableTools:  []fantasy.AgentTool{tool},
	})
}

func TestRepairToolCall_FillsMissingLabel(t *testing.T) {
	t.Parallel()

	tool := &schemaTool{info: fantasy.ToolInfo{
		Name:     "labelled",
		Required: []string{"description", "path"},
		Parameters: map[string]any{
			"description": map[string]any{"type": "string"},
			"path":        map[string]any{"type": "string"},
		},
	}}

	repaired, err := repairFor(t, `{"path":"/tmp/x"}`, tool)
	require.NoError(t, err)
	require.NotNil(t, repaired)

	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(repaired.Input), &input))
	require.Equal(t, "/tmp/x", input["path"], "the parameter that was sent must survive")
	require.Equal(t, "", input["description"], "a missing label is filled in rather than refused")
}

func TestRepairToolCall_LeavesRealParametersToTheModel(t *testing.T) {
	t.Parallel()

	tool := &schemaTool{info: fantasy.ToolInfo{
		Name:     "reader",
		Required: []string{"path"},
		Parameters: map[string]any{
			"path": map[string]any{"type": "string"},
		},
	}}

	// Inventing a path would read a file nobody asked for, so this call
	// stays rejected.
	repaired, err := repairFor(t, `{"description":"read it"}`, tool)
	require.Error(t, err)
	require.Nil(t, repaired)
}

func TestRepairToolCall_RepairsMalformedJSON(t *testing.T) {
	t.Parallel()

	tool := &schemaTool{info: fantasy.ToolInfo{
		Name:       "reader",
		Required:   []string{"path"},
		Parameters: map[string]any{"path": map[string]any{"type": "string"}},
	}}

	// Registering a repair function replaces the agent's built-in JSON
	// repair, so this path has to keep doing it.
	repaired, err := repairFor(t, `{"path": "/tmp/x",}`, tool)
	require.NoError(t, err)
	require.NotNil(t, repaired)

	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(repaired.Input), &input))
	require.Equal(t, "/tmp/x", input["path"])
}

func TestValidationHint_NamesWhatTheToolTakes(t *testing.T) {
	t.Parallel()

	info := fantasy.ToolInfo{
		Name:     "reader",
		Required: []string{"path"},
		Parameters: map[string]any{
			"path":   map[string]any{"type": "string"},
			"offset": map[string]any{"type": "integer"},
			"limit":  map[string]any{"type": "integer"},
		},
	}

	hint := ValidationHint(info, errors.New(validationErrPrefix+"path"))
	require.Contains(t, hint, "reader requires: path.")
	require.Contains(t, hint, "It also takes: limit, offset.")
	require.Contains(t, hint, `{"path": "<path>"}`)

	require.Empty(t,
		ValidationHint(info, errors.New("open /tmp/x: no such file or directory")),
		"a tool that failed at its job is not a schema problem",
	)
}

func TestIsValidationError(t *testing.T) {
	t.Parallel()

	require.True(t, IsValidationError(errors.New(validationErrPrefix+"command")))
	require.False(t, IsValidationError(errors.New("exit status 1")))
	require.False(t, IsValidationError(nil))
}
