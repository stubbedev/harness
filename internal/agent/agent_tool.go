package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"charm.land/fantasy"
	"golang.org/x/sync/errgroup"

	"github.com/stubbedev/harness/internal/agent/prompt"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/skills"
	"github.com/stubbedev/harness/internal/subagents"
)

//go:embed templates/agent_tool.md
var agentToolDescription string

// AgentParams is the shape consumed by UI tool-call renderers when displaying
// historical agent tool invocations. New tool-call inputs decode with
// AgentDispatchParams; AgentParams stays wire-compatible so older inputs still
// decode cleanly.
type AgentParams struct {
	SubagentType string `json:"subagent_type,omitempty"`
	Prompt       string `json:"prompt" description:"The task for the agent to perform"`
}

// AgentDispatchParams is the input to the dispatcher agent tool.
type AgentDispatchParams struct {
	SubagentType string `json:"subagent_type,omitempty"`
	Prompt       string `json:"prompt"`
	Background   bool   `json:"background,omitempty"`
}

const (
	AgentToolName = "agent"
)

// dispatcherTool implements fantasy.AgentTool with a dynamically-built schema.
type dispatcherTool struct {
	info         fantasy.ToolInfo
	dispatch     func(ctx context.Context, params AgentDispatchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error)
	providerOpts fantasy.ProviderOptions
}

func (d *dispatcherTool) Info() fantasy.ToolInfo                          { return d.info }
func (d *dispatcherTool) ProviderOptions() fantasy.ProviderOptions        { return d.providerOpts }
func (d *dispatcherTool) SetProviderOptions(opts fantasy.ProviderOptions) { d.providerOpts = opts }
func (d *dispatcherTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	var params AgentDispatchParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return fantasy.NewTextErrorResponse("invalid parameters: " + err.Error()), nil
	}
	return d.dispatch(ctx, params, call)
}

// findSubagentByName returns the active subagent with the given name, or nil
// when none matches.
func findSubagentByName(active []*subagents.Subagent, name string) *subagents.Subagent {
	for _, sa := range active {
		if sa.Name == name {
			return sa
		}
	}
	return nil
}

// describeSubagentForEnum renders one line of the subagent_type enum's
// description. The model and effort are included alongside the description
// because they answer a different question than it does: the description says
// whether a subagent fits the task, the model says whether dispatching it is
// cheap enough to fan several out. Mirrors subagents.ToPromptXML, which feeds
// the same two facts to the coder system prompt.
func describeSubagentForEnum(sa *subagents.Subagent) string {
	attrs := "model: " + sa.ModelLabel()
	if sa.Effort != "" {
		attrs += ", effort: " + sa.Effort
	}
	line := fmt.Sprintf("%s (%s): %s", sa.Name, attrs, sa.Description)
	if sa.IsCheap() {
		line += " [cheap: prefer fanning several out in parallel]"
	}
	return line
}

// buildAgentDispatchInfo builds the ToolInfo for the agent dispatcher tool with
// a dynamic subagent_type enum derived from the currently active subagents.
func buildAgentDispatchInfo(activeSubagents []*subagents.Subagent) fantasy.ToolInfo {
	enumValues := []string{config.AgentTask, config.AgentFast}
	// A subagent whose name collides with a built-in type is unreachable —
	// dispatch resolves built-ins first — so it is left out of the enum
	// entirely rather than listed as a choice that silently runs something
	// else.
	reachable := make([]*subagents.Subagent, 0, len(activeSubagents))
	for _, sa := range activeSubagents {
		if sa.Name == config.AgentTask || sa.Name == config.AgentFast {
			continue
		}
		reachable = append(reachable, sa)
		enumValues = append(enumValues, sa.Name)
	}

	typeDesc := `The type of agent to use. Lean light: the default is the small model, and the large model is an escalation you opt into.
- "fast": the same read-and-edit tools on the small model, and the default when subagent_type is omitted. Use for any well-scoped lookup, survey, or edit piece, and dispatch many in the same message — it is cheap enough that splitting work across several of them beats doing it yourself.
- "task": the same read-and-edit tools on the large model. Reserve for the genuinely open-ended piece that needs judgment — when a cheap pass would likely come back wrong or useless. Reaching for it by habit defeats its cost.`
	if len(reachable) > 0 {
		lines := make([]string, 0, len(reachable))
		for _, sa := range reachable {
			lines = append(lines, "- "+describeSubagentForEnum(sa))
		}
		typeDesc += "\n\nAvailable specialized agents:\n" + strings.Join(lines, "\n")
	}

	return fantasy.ToolInfo{
		Name:        AgentToolName,
		Description: agentToolDescription,
		Parameters: map[string]any{
			"subagent_type": map[string]any{
				"type":        "string",
				"enum":        enumValues,
				"description": typeDesc,
			},
			"prompt": map[string]any{
				"type":        "string",
				"description": "The task for the agent to perform",
			},
			"background": map[string]any{
				"type":        "boolean",
				"description": "Start the agent in the background and return immediately with a handle instead of waiting for its result. Messages it sends you arrive as new user messages between your steps while it runs; collect its result with the wait tool using the handle. Default false: the call blocks and returns the agent's final output.",
			},
		},
		Required: []string{"prompt"},
		Parallel: true,
	}
}

// lazyAgent memoizes one built-in sub-agent (task, fast) so the first dispatch
// pays for the build and later ones reuse it. The mutex serializes the
// concurrent dispatches this tool allows (Parallel: true) onto one build, but
// unlike sync.Once a failed build does not stick: the next dispatch retries.
type lazyAgent struct {
	coord  *coordinator
	prompt *prompt.Prompt
	cfg    config.Agent
	model  subagentModel

	mu    sync.Mutex
	agent SessionAgent
	built bool
}

// get returns the memoized agent, building it on first call.
func (l *lazyAgent) get(ctx context.Context) (SessionAgent, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.built {
		return l.agent, nil
	}
	var wg errgroup.Group
	agent, err := l.coord.buildAgent(ctx, l.prompt, l.cfg, true, l.model, &wg)
	if err == nil {
		err = wg.Wait()
	}
	if err != nil {
		// Leave built unset so a transient build failure (a cancelled dispatch
		// context, a provider hiccup) is retried by the next dispatch instead
		// of sticking for the tool's whole lifetime.
		return nil, err
	}
	l.agent = agent
	l.built = true
	return l.agent, nil
}

// agentTool builds the dispatcher tool. The context parameter is retained for
// call-site symmetry with the other buildTools helpers; the task agent is now
// built from the dispatch context instead, so nothing here consumes it.
func (c *coordinator) agentTool(_ context.Context) (fantasy.AgentTool, error) {
	taskCfg, ok := c.cfg.Config().Agents[config.AgentTask]
	if !ok {
		return nil, errors.New("task agent not configured")
	}
	coderCfg, ok := c.cfg.Config().Agents[config.AgentCoder]
	if !ok {
		return nil, errors.New("coder agent not configured")
	}
	fastCfg, ok := c.cfg.Config().Agents[config.AgentFast]
	if !ok {
		return nil, errors.New("fast agent not configured")
	}
	taskPr, err := taskPrompt(prompt.WithWorkingDir(c.cfg.WorkingDir()))
	if err != nil {
		return nil, err
	}
	fastPr, err := fastPrompt(prompt.WithWorkingDir(c.cfg.WorkingDir()))
	if err != nil {
		return nil, err
	}
	// The built-in agents are built on first dispatch, not here. Two reasons
	// they do not go on c.readyWg: UpdateModels rebuilds this tool at the
	// start of every turn — after that turn's readyWg.Wait — so a
	// readyWg-spawned build could still be pending when a dispatch runs
	// (starting the agent promptless/toolless), and a build failure would
	// stick in readyWg, failing every later turn.
	//
	// They are not built eagerly here either. buildAgent spawns a full skills
	// discovery walk plus an MCP-init wait, and nothing joins those goroutines
	// unless a dispatch actually happens — so eagerly building meant every
	// turn started a generation of work that the great majority of turns threw
	// away, with no backpressure across a burst of turns. Building on demand
	// makes an unused tool free and matches the subagent dispatch path below,
	// which also builds at dispatch time.
	builtins := map[string]*lazyAgent{
		config.AgentTask: {coord: c, prompt: taskPr, cfg: taskCfg},
		// The fast agent is the task agent on the small model: the `small`
		// alias routes buildAgent to the globally selected small model, the
		// same one the coordinator already builds for summarization.
		config.AgentFast: {coord: c, prompt: fastPr, cfg: fastCfg, model: subagentModel{Model: subagents.ModelAliasSmall}},
	}

	// The subagent_type enum is a point-in-time snapshot baked into the tool
	// schema; a Library reload won't refresh it. Dispatch lookups use the live
	// list (activeSubagentsList) so a since-removed name fails cleanly and a
	// newly added one still resolves — the enum is advisory only.
	info := buildAgentDispatchInfo(c.activeSubagentsList())

	return &dispatcherTool{
		info: info,
		dispatch: func(ctx context.Context, params AgentDispatchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Prompt == "" {
				return fantasy.NewTextErrorResponse("prompt is required"), nil
			}

			sessionID := tools.GetSessionFromContext(ctx)
			if sessionID == "" {
				// Tool-error responses, never bare errors: a bare error from
				// any tool in a batch aborts the whole step in fantasy,
				// discarding the sibling dispatches' results and failing the
				// turn. As a tool error the model sees the failure, the
				// batch completes, and the turn continues.
				return fantasy.NewTextErrorResponse("session id missing from context"), nil
			}
			agentMessageID := tools.GetMessageFromContext(ctx)
			if agentMessageID == "" {
				return fantasy.NewTextErrorResponse("agent message id missing from context"), nil
			}

			// Every dispatch below runs a whole child session, so the
			// concurrency slot is taken here — before any build work — and
			// held for the run. Over the limit this blocks rather than
			// failing, so a wide fan-out completes in waves. A background
			// dispatch transfers the slot to the run instead of holding it
			// for the tool call: the child holds it until it finishes.
			release, slotErr := c.acquireDispatchSlot(ctx)
			if slotErr != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("acquire dispatch slot: %v", slotErr)), nil
			}

			// dispatchRun runs the resolved agent as this call's dispatch.
			// Both blocking and background forms share it; only the slot
			// ownership differs.
			handedOff := false
			dispatchRun := func(agent SessionAgent, title, name, color, model string) (fantasy.ToolResponse, error) {
				runParams := subAgentParams{
					Agent:          agent,
					SessionID:      sessionID,
					AgentMessageID: agentMessageID,
					ToolCallID:     call.ID,
					Prompt:         params.Prompt,
					SessionTitle:   title,
					AgentName:      name,
					AgentColor:     color,
					AgentModel:     model,
					Background:     params.Background,
				}
				if params.Background {
					runParams.ReleaseSlot = release
					handedOff = true
				}
				return c.runSubAgent(ctx, runParams)
			}
			if params.Background {
				defer func() {
					// Once dispatchRun hands the slot to the run, the
					// background goroutine owns releasing it. If dispatch
					// never got that far — unknown type, build failure —
					// give it back here. release is once-wrapped, so this
					// is a no-op when a failure path already released.
					if !handedOff {
						release()
					}
				}()
			} else {
				defer release()
			}

			subagentType := params.SubagentType
			if subagentType == "" {
				// Dispatch leans light: an omitted type runs the cheap fast
				// agent on the small model, and task is an explicit
				// escalation the model must ask for by name.
				subagentType = config.AgentFast
			}
			if builtin, ok := builtins[subagentType]; ok {
				builtAgent, err := builtin.get(ctx)
				if err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("build %s agent: %v", subagentType, err)), nil
				}
				return dispatchRun(builtAgent, "New Agent Session", subagentType, subagents.AutoColor(subagentType), builtAgent.Model().ModelCfg.Model)
			}

			sa := findSubagentByName(c.activeSubagentsList(), subagentType)
			if sa == nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("unknown subagent type: %q", subagentType)), nil
			}

			agentCfg := sa.ToConfigAgent(coderCfg)
			// Config-driven setup failures (prompt build, model/provider that
			// passed discovery but fails at build) are surfaced as tool-error
			// responses so the parent agent can report them and continue; a
			// bare error would abort the whole turn.
			activeSkills := c.activeSkillsList()
			subPr, err := subagentPrompt(
				sa,
				activeSkills,
				prompt.WithWorkingDir(c.cfg.WorkingDir()),
				// Reuse the skills the coordinator already holds instead of
				// letting prompt.Build re-walk every configured skills path.
				// This tool is Parallel, so N concurrent dispatches would
				// otherwise each pay a full recursive walk before their first
				// token.
				prompt.WithAvailableSkillsXML(skills.ToPromptXML(activeSkills)),
			)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("build subagent prompt %q: %v", sa.Name, err)), nil
			}
			// Build on a local group and wait before running: the agent must
			// not start promptless/toolless, and a build failure must land
			// here as a tool error rather than in the coordinator-wide
			// readyWg, whose sticky error would fail every subsequent turn.
			var buildWg errgroup.Group
			agent, err := c.buildAgent(ctx, subPr, agentCfg, true, subagentModel{Effort: sa.Effort, Model: sa.Model, Provider: sa.Provider}, &buildWg)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("build subagent %q: %v", sa.Name, err)), nil
			}
			if err := buildWg.Wait(); err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("build subagent %q: %v", sa.Name, err)), nil
			}

			return dispatchRun(agent, sa.Name+" Agent Session", sa.Name, sa.ResolvedColor(), agent.Model().ModelCfg.Model)
		},
	}, nil
}
