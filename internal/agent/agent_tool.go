package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/stubbedev/harness/internal/toolname"

	"charm.land/fantasy"
	"charm.land/fantasy/jsonrepair"
	"golang.org/x/sync/errgroup"

	"github.com/stubbedev/harness/internal/agent/prompt"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/message"
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

// AgentDispatchParams is the input to the dispatcher agent tool. With a
// prompt it dispatches; without one it used to wait for background
// agents, which the wait tool now does (see coordinator.Wait). A
// promptless call is still classified as AgentCallWait so historical
// transcripts and their renderers keep decoding.
type AgentDispatchParams struct {
	SubagentType string `json:"subagent_type,omitempty"`
	Prompt       string `json:"prompt,omitempty"`
	Isolation    string `json:"isolation,omitempty"`
	// Blocking opts into waiting for the child: the tool call returns its
	// result inline once the run finishes. The default (omitted or false)
	// dispatches in the background — the call returns a handle immediately
	// and the orchestrator keeps working while the child runs.
	Blocking bool `json:"blocking,omitempty"`
	// Handles names the background agents a message or cancel applies to.
	Handles []string `json:"handles,omitempty"`
	// Message is text for running background agents, delivered to each
	// as a user message before its next step.
	Message string `json:"message,omitempty"`
	// Cancel stops the running background agents named in Handles.
	Cancel bool `json:"cancel,omitempty"`
}

// AgentCallKind is the form a dispatcher tool call takes, decided by
// which of its mutually exclusive fields is set.
type AgentCallKind int

const (
	AgentCallDispatch AgentCallKind = iota
	AgentCallWait
	AgentCallMessage
	AgentCallCancel
)

// Kind classifies complete params.
func (p AgentDispatchParams) Kind() AgentCallKind {
	switch {
	case p.Prompt != "":
		return AgentCallDispatch
	case p.Message != "":
		return AgentCallMessage
	case p.Cancel:
		return AgentCallCancel
	default:
		return AgentCallWait
	}
}

// validateForm rejects a call that sets more than one of prompt,
// message and cancel, which would leave its intent ambiguous.
func (p AgentDispatchParams) validateForm() error {
	set := 0
	for _, isSet := range []bool{p.Prompt != "", p.Message != "", p.Cancel} {
		if isSet {
			set++
		}
	}
	if set > 1 {
		return errors.New(`set at most one of "prompt" (dispatch), "message" (message running agents) and "cancel" (cancel running agents)`)
	}
	return nil
}

const (
	AgentToolName = toolname.Agent
)

// DecodeAgentCall decodes a dispatcher tool call as far as its input has
// streamed and classifies it. An unfinished input is repaired into the
// JSON it is a prefix of, so a dispatch is recognised, with its prompt so
// far, from the first characters of the prompt. decided is false while a
// streaming call has not yet shown its form: a wait is only known once
// the input is complete without a prompt, message or cancel. Single
// source for the task strip's rows and the transcript's renderer choice.
func DecodeAgentCall(call message.ToolCall) (params AgentDispatchParams, kind AgentCallKind, decided bool) {
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil && !call.Finished {
		if repaired, repairErr := jsonrepair.RepairJSON(call.Input); repairErr == nil {
			_ = json.Unmarshal([]byte(repaired), &params)
		}
	}
	kind = params.Kind()
	return params, kind, call.Finished || kind != AgentCallWait
}

// IsAgentControlCall reports whether a tool call is the dispatcher tool
// acting on agents already dispatched (waiting for, messaging or
// cancelling them) rather than dispatching a new one. A call that has not
// shown its form yet is neither.
func IsAgentControlCall(call message.ToolCall) bool {
	if call.Name != AgentToolName {
		return false
	}
	_, kind, decided := DecodeAgentCall(call)
	return decided && kind != AgentCallDispatch
}

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
	if resp, ok := decodeToolParams(call, &params); !ok {
		return resp, nil
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
// whether a subagent fits the task, the model says what a dispatch of it
// spends. Mirrors subagents.ToPromptXML, which feeds the same two facts to
// the coder system prompt.
func describeSubagentForEnum(sa *subagents.Subagent) string {
	attrs := "model: " + sa.ModelLabel()
	if sa.Effort != "" {
		attrs += ", effort: " + sa.Effort
	}
	line := fmt.Sprintf("%s (%s): %s", sa.Name, attrs, sa.Description)
	if sa.IsCheap() {
		line += " [cheap: runs on the small model]"
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

	typeDesc := `Which agent runs the prompt. Defaults to "fast" (small model).`
	if len(reachable) > 0 {
		lines := make([]string, 0, len(reachable))
		for _, sa := range reachable {
			lines = append(lines, "- "+describeSubagentForEnum(sa))
		}
		typeDesc += "\n\nSpecialized agents:\n" + strings.Join(lines, "\n")
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
				"description": "The task for the agent to perform.",
			},
			"isolation": map[string]any{
				"type":        "string",
				"enum":        []string{"worktree"},
				"description": "Dispatch only: run in a separate Git worktree seeded with the current checkout. Changed worktrees and a patch are retained for inspection; no automatic merge. Full trust, not a sandbox.",
			},
			"blocking": map[string]any{
				"type":        "boolean",
				"description": "Wait for this sub-agent and return its result in this call instead of a handle. Set true only when no further step is possible without the result.",
			},
			"handles": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Handles returned by earlier calls, for `message` or `cancel`.",
			},
			"message": map[string]any{
				"type":        "string",
				"description": "Instead of dispatching, send this text to running background agents: each reads it as a user message before its next step. A finished or unknown handle is an error.",
			},
			"cancel": map[string]any{
				"type":        "boolean",
				"description": "Instead of dispatching, cancel the running or queued background agents in `handles`. Their results are still collected by the wait tool.",
			},
		},
		// Every field is optional -- the dispatching form needs a prompt,
		// the waiting form needs nothing -- but the empty slice has to be
		// explicit: a nil Required marshals to JSON null, and the OpenAI
		// Responses API rejects the whole request with "Invalid schema for
		// function 'agent': None is not of type 'array'".
		Required: []string{},
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
	agent, err := l.coord.buildAgent(ctx, l.prompt, l.cfg, true, l.model, &wg, nil)
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
		dispatch: func(ctx context.Context, params AgentDispatchParams, call fantasy.ToolCall) (response fantasy.ToolResponse, dispatchErr error) {
			sessionID := tools.GetSessionFromContext(ctx)
			if sessionID == "" {
				// Tool-error responses, never bare errors: a bare error from
				// any tool in a batch aborts the whole step in fantasy,
				// discarding the sibling dispatches' results and failing the
				// turn. As a tool error the model sees the failure, the
				// batch completes, and the turn continues.
				return fantasy.NewTextErrorResponse("session id missing from context"), nil
			}
			if err := params.validateForm(); err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			switch params.Kind() {
			case AgentCallWait:
				return fantasy.NewTextErrorResponse("the agent tool no longer waits: call the wait tool with no wait_for to collect background jobs, timers and agents"), nil
			case AgentCallMessage:
				return c.messageSubagents(ctx, sessionID, params.Handles, params.Message), nil
			case AgentCallCancel:
				return c.cancelSubagents(sessionID, params.Handles), nil
			case AgentCallDispatch:
			}
			agentMessageID := tools.GetMessageFromContext(ctx)
			if agentMessageID == "" {
				return fantasy.NewTextErrorResponse("agent message id missing from context"), nil
			}

			if params.Isolation != "" && params.Isolation != "worktree" {
				return fantasy.NewTextErrorResponse("isolation must be worktree or omitted"), nil
			}

			if params.Blocking {
				release, slotErr := c.acquireDispatchSlot(ctx)
				if slotErr != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("acquire dispatch slot: %v", slotErr)), nil
				}
				defer release()
			}

			var isolated *isolatedDispatch
			workspaceTransferred := false
			defer func() {
				if isolated != nil && !workspaceTransferred {
					isolated.finish(&response)
				}
			}()
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
				}
				if isolated != nil {
					runParams.Prompt = isolated.prompt(c.cfg.WorkingDir(), params.Prompt)
					runParams.WorkspaceRoot = isolated.workspace.store.WorkingDir()
					runParams.FinishWorkspace = isolated.finish
					workspaceTransferred = true
				}
				if !params.Blocking {
					runParams.Background = true
				}
				return c.runSubAgent(ctx, runParams)
			}

			subagentType := params.SubagentType
			if subagentType == "" {
				// Dispatch leans light: an omitted type runs the cheap fast
				// agent on the small model, and task is an explicit
				// escalation the model must ask for by name.
				subagentType = config.AgentFast
			}
			if builtin, ok := builtins[subagentType]; ok {
				var builtAgent SessionAgent
				var err error
				if params.Isolation == "worktree" {
					isolated, err = c.prepareIsolatedDispatch(ctx)
					if err == nil {
						var pr *prompt.Prompt
						if subagentType == config.AgentFast {
							pr, err = fastPrompt(prompt.WithWorkingDir(isolated.workspace.store.WorkingDir()))
						} else {
							pr, err = taskPrompt(prompt.WithWorkingDir(isolated.workspace.store.WorkingDir()))
						}
						if err == nil {
							var group errgroup.Group
							builtAgent, err = c.buildAgent(ctx, pr, builtin.cfg, true, builtin.model, &group, isolated.workspace)
							if waitErr := group.Wait(); err == nil {
								err = waitErr
							}
						}
					}
				} else {
					builtAgent, err = builtin.get(ctx)
				}
				if err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("build %s agent: %v", subagentType, err)), nil
				}
				return dispatchRun(builtAgent, "New Agent Session", subagentType, subagents.AutoColor(subagentType), builtAgent.Model().ModelCfg.Model)
			}

			sa := findSubagentByName(c.activeSubagentsList(), subagentType)
			if sa == nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("unknown subagent type: %q", subagentType)), nil
			}

			workingDir := c.cfg.WorkingDir()
			var workspace *agentWorkspace
			if params.Isolation == "worktree" || sa.Isolation == "worktree" {
				var err error
				isolated, err = c.prepareIsolatedDispatch(ctx)
				if err != nil {
					return fantasy.NewTextErrorResponse(err.Error()), nil
				}
				workingDir = isolated.workspace.store.WorkingDir()
				workspace = isolated.workspace
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
				prompt.WithWorkingDir(workingDir),
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
			agent, err := c.buildAgent(ctx, subPr, agentCfg, true, subagentModel{Effort: sa.Effort, Model: sa.Model, Provider: sa.Provider}, &buildWg, workspace)
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
