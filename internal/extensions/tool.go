package extensions

import (
	"context"
	"encoding/json"

	"charm.land/fantasy"
	lua "github.com/yuin/gopher-lua"
)

// luaTool adapts a tool an extension registered to the agent tool
// interface. The schema is whatever the extension declared; the handler
// runs in the extension's VM, one call at a time.
type luaTool struct {
	spec            *toolSpec
	providerOptions fantasy.ProviderOptions
}

func (t *luaTool) Info() fantasy.ToolInfo {
	parameters := t.spec.parameters
	if parameters == nil {
		parameters = map[string]any{}
	}
	required := t.spec.required
	if required == nil {
		required = []string{}
	}
	return fantasy.ToolInfo{
		Name:        t.spec.name,
		Description: t.spec.description,
		Parameters:  parameters,
		Required:    required,
		Parallel:    t.spec.parallel,
	}
}

func (t *luaTool) ProviderOptions() fantasy.ProviderOptions { return t.providerOptions }

func (t *luaTool) SetProviderOptions(opts fantasy.ProviderOptions) { t.providerOptions = opts }

// Run hands the model's arguments to the Lua handler and renders what it
// returns. A handler may return a string, or a table carrying content,
// an error flag and metadata. An error raised inside the VM comes back
// as a tool error the model can read and recover from, rather than as a
// failure of the turn.
func (t *luaTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	in := t.spec.in

	var input any
	if call.Input != "" {
		if err := json.Unmarshal([]byte(call.Input), &input); err != nil {
			return fantasy.NewTextErrorResponse("invalid tool arguments: " + err.Error()), nil
		}
	}

	value, err := in.call(
		ctx,
		t.spec.fn,
		toLua(in.L, input),
		t.callInfo(ctx, call),
	)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}
	return toolResponse(value), nil
}

// callInfo builds the second argument every handler receives: what
// Harness knows about the call being made.
func (t *luaTool) callInfo(ctx context.Context, call fantasy.ToolCall) lua.LValue {
	in := t.spec.in
	tbl := in.L.CreateTable(0, 4)
	tbl.RawSetString("tool_call_id", lua.LString(call.ID))
	tbl.RawSetString("extension", lua.LString(in.ext.Name))
	tbl.RawSetString("working_dir", lua.LString(in.host.opts.WorkingDir))
	if fn := in.host.opts.SessionFromContext; fn != nil {
		tbl.RawSetString("session_id", lua.LString(fn(ctx)))
	}
	return tbl
}

// toolResponse renders a handler's return value.
func toolResponse(value lua.LValue) fantasy.ToolResponse {
	switch typed := value.(type) {
	case *lua.LNilType:
		return fantasy.NewTextResponse("")
	case lua.LString:
		return fantasy.NewTextResponse(string(typed))
	case *lua.LTable:
		content := tableString(typed, "content", "")
		if content == "" {
			content = tableString(typed, "text", "")
		}
		response := fantasy.NewTextResponse(content)
		if tableBool(typed, "is_error", false) || tableBool(typed, "error", false) {
			response.IsError = true
		}
		response.StopTurn = tableBool(typed, "stop_turn", false)
		if metadata, ok := typed.RawGetString("metadata").(*lua.LTable); ok {
			if data, err := json.Marshal(fromLua(metadata)); err == nil {
				response.Metadata = string(data)
			}
		}
		return response
	default:
		return fantasy.NewTextResponse(value.String())
	}
}
