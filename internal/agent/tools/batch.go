package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"charm.land/fantasy"
	"github.com/itchyny/gojq"
	"golang.org/x/sync/errgroup"
)

const (
	// BatchToolName is the tool name the model calls.
	BatchToolName = "batch"

	// maxBatchSteps bounds a single plan. A plan longer than this is
	// almost always a model looping rather than composing.
	maxBatchSteps = 32

	// maxFanOut bounds how many items one for_each may expand to. The
	// whole point of the tool is that a wide fan-out costs one context
	// entry, so the ceiling protects the tools being called, not the
	// context.
	maxFanOut = 256

	// batchParallelism bounds concurrent tool calls within one for_each,
	// except for the tools in serialTools, which run one at a time.
	batchParallelism = 8

	// maxBatchResultBytes caps what the tool returns to the model. A
	// plan whose return value blows past this has defeated its own
	// purpose, so say so rather than silently flooding the context.
	maxBatchResultBytes = 64 * 1024
)

//go:embed batch.md
var batchDescription string

// serialTools are the tools a for_each runs one item at a time. They
// share one piece of state across every call - the terminal session -
// so running several at once interleaves them rather than parallelising
// them.
var serialTools = map[string]bool{
	ShellToolName: true,
}

// BatchStep is one tool call, or one fan-out of the same tool call over a
// list of items.
type BatchStep struct {
	ID      string         `json:"id" description:"Step id; its output binds to $<id>."`
	Tool    string         `json:"tool" description:"Tool to call."`
	Input   map[string]any `json:"input,omitempty" description:"Literal JSON input."`
	InputJQ string         `json:"input_jq,omitempty" description:"jq producing the input object; merges over input."`
	ForEach string         `json:"for_each,omitempty" description:"jq listing items to fan out over; $item and $index bound."`
	When    string         `json:"when,omitempty" description:"jq gate; falsy skips the step."`
	OnError string         `json:"on_error,omitempty" description:"'fail' (default) aborts the batch on a tool error. 'collect' records {\"error\": \"...\"} as the result and keeps going."`
}

// BatchParams is the batch tool's input.
type BatchParams struct {
	Steps  []BatchStep `json:"steps" description:"Steps to run in order."`
	Return string      `json:"return,omitempty" description:"jq over the step outputs; the only thing that enters the conversation."`
}

// BatchResponseMetadata reports what the plan actually did, for the UI and
// for anyone reading the transcript. The step outputs themselves are
// deliberately absent: keeping them out of the conversation is the point.
type BatchResponseMetadata struct {
	Steps     int            `json:"steps"`
	ToolCalls int            `json:"tool_calls"`
	Errors    int            `json:"errors,omitempty"`
	PerStep   map[string]int `json:"per_step,omitempty"`
}

const (
	onErrorFail    = "fail"
	onErrorCollect = "collect"
)

// NewBatchTool returns the batch tool. The tools it may call are resolved
// from siblings, which should be the same slice the agent was given -
// already wrapped in whatever decorators apply, so a call made from inside
// a plan goes through the same path as one the model makes directly.
func NewBatchTool(siblings func() []fantasy.AgentTool) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		BatchToolName,
		batchDescription,
		func(ctx context.Context, params BatchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return runBatch(ctx, params, siblings())
		},
	)
}

// batchEnv holds the jq variable environment: one binding per completed
// step, plus the loop bindings while a for_each is running.
type batchEnv struct {
	names  []string
	values []any
}

// set binds a value to a jq variable. Callers pass the bare name; gojq
// wants the leading "$" on the names it compiles against, so this is the
// one place that knows about the sigil.
func (e *batchEnv) set(name string, value any) {
	name = "$" + name
	if i := slices.Index(e.names, name); i >= 0 {
		e.values[i] = value
		return
	}
	e.names = append(e.names, name)
	e.values = append(e.values, value)
}

// with returns a copy of the environment with extra bindings applied, so
// concurrent for_each items never share a mutable environment.
func (e *batchEnv) with(extra map[string]any) *batchEnv {
	out := &batchEnv{
		names:  slices.Clone(e.names),
		values: slices.Clone(e.values),
	}
	for _, name := range slices.Sorted(maps.Keys(extra)) {
		out.set(name, extra[name])
	}
	return out
}

// eval runs a jq expression against the environment and returns every
// value it produces. The input document is null: everything a step needs
// arrives through variables, so there is no ambiguity about what "." is.
func (e *batchEnv) eval(expr string) ([]any, error) {
	query, err := gojq.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", expr, err)
	}
	code, err := gojq.Compile(query, gojq.WithVariables(e.names))
	if err != nil {
		return nil, fmt.Errorf("compile %q: %w", expr, err)
	}
	var out []any
	iter := code.Run(nil, e.values...)
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, ok := v.(error); ok {
			// A bare `halt` ends the expression without being a failure.
			var halt *gojq.HaltError
			if errors.As(err, &halt) && halt.Value() == nil {
				break
			}
			return nil, fmt.Errorf("evaluate %q: %w", expr, err)
		}
		out = append(out, v)
	}
	return out, nil
}

// evalOne runs a jq expression expected to produce exactly one value.
func (e *batchEnv) evalOne(expr string) (any, error) {
	vals, err := e.eval(expr)
	if err != nil {
		return nil, err
	}
	switch len(vals) {
	case 0:
		return nil, nil
	case 1:
		return vals[0], nil
	default:
		return nil, fmt.Errorf("expression %q produced %d values, expected one", expr, len(vals))
	}
}

func runBatch(ctx context.Context, params BatchParams, siblings []fantasy.AgentTool) (fantasy.ToolResponse, error) {
	if len(params.Steps) == 0 {
		return fantasy.NewTextErrorResponse("steps is required and must not be empty"), nil
	}
	if len(params.Steps) > maxBatchSteps {
		return fantasy.NewTextErrorResponse(fmt.Sprintf(
			"too many steps: %d (max %d)", len(params.Steps), maxBatchSteps)), nil
	}

	registry := make(map[string]fantasy.AgentTool, len(siblings))
	for _, t := range siblings {
		registry[t.Info().Name] = t
	}

	// Everything checkable is checked before the first tool runs. A typo
	// in the last step's tool name or in `return` used to surface only
	// after every earlier step had made its calls, so the plan paid for
	// forty file reads and then threw them away.
	if msg := preflight(params, registry); msg != "" {
		return fantasy.NewTextErrorResponse(msg), nil
	}

	env := &batchEnv{}
	meta := BatchResponseMetadata{Steps: len(params.Steps), PerStep: map[string]int{}}
	var lastID string

	for _, step := range params.Steps {
		tool := registry[step.Tool]
		output, calls, errs, err := runStep(ctx, step, tool, env)
		if err != nil {
			return fantasy.NewTextErrorResponse(fmt.Sprintf("step %q: %s%s", step.ID, err, env.shapeHint())), nil
		}
		meta.ToolCalls += calls
		meta.Errors += errs
		if calls > 0 {
			meta.PerStep[step.ID] = calls
		}
		env.set(step.ID, output)
		lastID = step.ID
	}

	returnExpr := params.Return
	if returnExpr == "" {
		returnExpr = "$" + lastID
	}
	values, err := env.eval(returnExpr)
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("return: %s%s", err, env.shapeHint())), nil
	}

	var result any
	switch len(values) {
	case 0:
		result = nil
	case 1:
		result = values[0]
	default:
		result = values
	}

	rendered, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("return: result is not JSON-encodable: %s", err)), nil
	}
	if len(rendered) > maxBatchResultBytes {
		return fantasy.NewTextErrorResponse(fmt.Sprintf(
			"return: result is %d bytes, over the %d byte limit. Narrow the 'return' expression - "+
				"the point of batch is that only the filtered result enters the conversation.",
			len(rendered), maxBatchResultBytes)), nil
	}

	return fantasy.WithResponseMetadata(fantasy.NewTextResponse(string(rendered)), meta), nil
}

// runStep executes one step, fanned out over for_each when present, and
// returns its output value along with how many tool calls it made and how
// many of those errored.
func runStep(ctx context.Context, step BatchStep, tool fantasy.AgentTool, env *batchEnv) (any, int, int, error) {
	if step.When != "" {
		gate, err := env.evalOne(step.When)
		if err != nil {
			return nil, 0, 0, err
		}
		if !truthy(gate) {
			return nil, 0, 0, nil
		}
	}

	if step.ForEach == "" {
		input, err := stepInput(step, env)
		if err != nil {
			return nil, 0, 0, err
		}
		value, failed, err := callTool(ctx, tool, step, input)
		if err != nil {
			return nil, 1, 0, err
		}
		return value, 1, boolToInt(failed), nil
	}

	items, err := env.eval(step.ForEach)
	if err != nil {
		return nil, 0, 0, err
	}
	// A for_each over a single array value iterates that array; this is
	// what `$prev | .[].id` and `$prev` both intuitively mean.
	if len(items) == 1 {
		if arr, ok := items[0].([]any); ok {
			items = arr
		}
	}
	if len(items) > maxFanOut {
		return nil, 0, 0, fmt.Errorf("for_each produced %d items (max %d)", len(items), maxFanOut)
	}
	if len(items) == 0 {
		return []any{}, 0, 0, nil
	}

	results := make([]any, len(items))
	failures := make([]bool, len(items))
	g, gctx := errgroup.WithContext(ctx)
	// Tools that own a shared, stateful resource run one at a time. The shell
	// is the one that matters: every call goes to the same persistent
	// terminal session, so a fan-out either queues behind itself or
	// spills into sibling shells that have none of the first one's cd
	// and exports - and either way the results stop lining up with the
	// commands that produced them.
	if serialTools[step.Tool] {
		g.SetLimit(1)
	} else {
		g.SetLimit(batchParallelism)
	}
	var mu sync.Mutex

	for i, item := range items {
		g.Go(func() error {
			itemEnv := env.with(map[string]any{"item": item, "index": i})
			input, err := stepInput(step, itemEnv)
			if err != nil {
				return fmt.Errorf("item %d: %w", i, err)
			}
			value, failed, err := callTool(gctx, tool, step, input)
			if err != nil {
				return fmt.Errorf("item %d: %w", i, err)
			}
			mu.Lock()
			results[i] = value
			failures[i] = failed
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, len(items), 0, err
	}

	errs := 0
	for _, failed := range failures {
		if failed {
			errs++
		}
	}
	return results, len(items), errs, nil
}

// callTool invokes one tool and decodes its response. The returned bool
// reports whether the tool answered with an error that on_error: collect
// swallowed; a returned error means the step (and the batch) cannot
// continue.
func callTool(ctx context.Context, tool fantasy.AgentTool, step BatchStep, input map[string]any) (any, bool, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, false, fmt.Errorf("encode input: %w", err)
	}

	resp, err := tool.Run(ctx, fantasy.ToolCall{
		ID:    "batch:" + step.ID,
		Name:  step.Tool,
		Input: string(raw),
	})
	if err != nil {
		if step.OnError == onErrorCollect {
			return map[string]any{"error": err.Error()}, true, nil
		}
		return nil, false, fmt.Errorf("%s: %w (set on_error: collect to continue past this)", step.Tool, err)
	}
	if resp.IsError {
		if step.OnError == onErrorCollect {
			return map[string]any{"error": resp.Content}, true, nil
		}
		return nil, false, fmt.Errorf("%s: %s (set on_error: collect to continue past this)", step.Tool, resp.Content)
	}

	return decodeToolContent(resp.Content), false, nil
}

// decodeToolContent turns a tool's text response into a jq-addressable
// value: parsed JSON when it is JSON, the plain string otherwise. Tools
// here answer in both shapes and a plan should be able to reach into
// either without knowing which it got.
func decodeToolContent(content string) any {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	switch trimmed[0] {
	case '{', '[':
		var v any
		if err := json.Unmarshal([]byte(trimmed), &v); err == nil {
			return v
		}
	}
	return content
}

// stepInput builds a step's tool input from its literal input and its
// input_jq expression, the latter shallow-merging over the former.
func stepInput(step BatchStep, env *batchEnv) (map[string]any, error) {
	input := map[string]any{}
	maps.Copy(input, step.Input)

	if step.InputJQ == "" {
		return input, nil
	}
	v, err := env.evalOne(step.InputJQ)
	if err != nil {
		return nil, err
	}
	patch, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("input_jq must produce an object, got %T", v)
	}
	maps.Copy(input, patch)
	return input, nil
}

// truthy follows jq's rule: everything but false and null.
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	default:
		return true
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// validStepID reports whether id can be used as a jq variable name.
func validStepID(id string) bool {
	for i, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return id != ""
}

func availableToolNames(registry map[string]fantasy.AgentTool) []string {
	return slices.Sorted(maps.Keys(registry))
}

// preflight checks everything about a plan that can be known before any
// tool runs: step identity, tool names, on_error spellings, and that
// every jq expression parses and only references variables that a step
// earlier in the plan actually binds. It returns the message to hand
// back, or "" when the plan is sound.
//
// Doing this up front is the difference between a typo costing nothing
// and costing every tool call the plan made before reaching it.
func preflight(params BatchParams, registry map[string]fantasy.AgentTool) string {
	seen := make(map[string]bool, len(params.Steps))
	// bound tracks the variables in scope for the step being checked:
	// the ids bound so far, plus the two a for_each step binds for its
	// own expressions.
	bound := map[string]bool{}

	for i, step := range params.Steps {
		switch {
		case step.ID == "":
			return fmt.Sprintf("step %d: id is required", i)
		case !validStepID(step.ID):
			return fmt.Sprintf("step %q: id must be a jq-safe identifier (letters, digits, underscore; not starting with a digit)", step.ID)
		case seen[step.ID]:
			return fmt.Sprintf("step %q: duplicate id", step.ID)
		}
		seen[step.ID] = true

		// Validate rather than defaulting: silently treating a typo as
		// "fail" would abort a plan the author meant to be resilient.
		if step.OnError != "" && step.OnError != onErrorFail && step.OnError != onErrorCollect {
			return fmt.Sprintf("step %q: on_error must be %q or %q, got %q",
				step.ID, onErrorFail, onErrorCollect, step.OnError)
		}

		if _, ok := registry[step.Tool]; !ok {
			return fmt.Sprintf("step %q: unknown tool %q. Available: %s",
				step.ID, step.Tool, strings.Join(availableToolNames(registry), ", "))
		}

		// when and for_each run before the item bindings exist; input_jq
		// runs once per item and may use them.
		for _, expr := range []string{step.When, step.ForEach} {
			if msg := checkExpr(step.ID, expr, bound); msg != "" {
				return msg
			}
		}
		withItem := maps.Clone(bound)
		if step.ForEach != "" {
			withItem["$item"] = true
			withItem["$index"] = true
		}
		if msg := checkExpr(step.ID, step.InputJQ, withItem); msg != "" {
			return msg
		}

		bound["$"+step.ID] = true
	}

	if msg := checkExpr("return", params.Return, bound); msg != "" {
		return msg
	}
	return ""
}

// checkExpr reports what is wrong with one jq expression, or "" when it
// parses and every variable it names is in scope. An out-of-scope
// variable is almost always a step id misspelled or referenced before it
// runs, so the message names what is actually available.
func checkExpr(where, expr string, bound map[string]bool) string {
	if expr == "" {
		return ""
	}
	query, err := gojq.Parse(expr)
	if err != nil {
		return fmt.Sprintf("%s: parse %q: %s", where, expr, err)
	}
	names := slices.Sorted(maps.Keys(bound))
	if _, err := gojq.Compile(query, gojq.WithVariables(names)); err != nil {
		return fmt.Sprintf("%s: %s. In scope here: %s", where, err, inScope(names))
	}
	return ""
}

// inScope renders the variable list for an error message.
func inScope(names []string) string {
	if len(names) == 0 {
		return "(nothing yet - this is the first step)"
	}
	return strings.Join(names, ", ")
}

// shapeHint describes the shape of each bound step output, appended to a
// jq failure so the next attempt is written against what the tools
// actually returned rather than against a guess. Without it the only way
// to learn a tool's response shape is to run it outside a plan first.
func (e *batchEnv) shapeHint() string {
	if len(e.names) == 0 {
		return ""
	}
	parts := make([]string, 0, len(e.names))
	for i, name := range e.names {
		parts = append(parts, name+": "+describeShape(e.values[i]))
	}
	return "\nStep outputs so far - " + strings.Join(parts, "; ")
}

// describeShape renders one value as its type and, for the shapes jq
// expressions trip over, enough structure to fix the expression: an
// object's keys and an array's length and element shape.
func describeShape(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case map[string]any:
		keys := slices.Sorted(maps.Keys(t))
		if len(keys) > 12 {
			keys = append(keys[:12], "…")
		}
		return "object{" + strings.Join(keys, ", ") + "}"
	case []any:
		if len(t) == 0 {
			return "array[0]"
		}
		return fmt.Sprintf("array[%d] of %s", len(t), describeShape(t[0]))
	case string:
		if len(t) > 40 {
			return fmt.Sprintf("string(%d chars)", len(t))
		}
		return fmt.Sprintf("string(%q)", t)
	case bool:
		return "boolean"
	default:
		return "number"
	}
}
