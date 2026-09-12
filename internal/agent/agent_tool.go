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
	"github.com/stubbedev/harness/internal/permission"
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

// subagentSessionSetup returns a SessionSetup callback that applies the
// subagent's permission mode to the freshly-created sub-session. Returns
// nil when no setup is needed.
func (c *coordinator) subagentSessionSetup(sa *subagents.Subagent) func(sessionID string) {
	if sa.PermissionMode != subagents.PermissionModeBypassPermissions {
		return nil
	}
	return func(sessionID string) {
		c.permissions.AutoApproveSession(sessionID)
	}
}

// confirmBypassPermissions gates permissionMode: bypassPermissions behind an
// explicit user confirmation for every dispatch of a subagent that is not
// user-scoped. A repository can ship a subagent definition with a description
// crafted to get auto-dispatched, so repo-provided bypass must never
// auto-approve a whole child session without the user seeing it. Returns
// (zero, true) when dispatch may proceed and (denial response, false)
// otherwise. Yolo mode and the standard allowlist/auto-approve paths are
// honored by the permission service itself.
func (c *coordinator) confirmBypassPermissions(ctx context.Context, sa *subagents.Subagent, sessionID, toolCallID string) (fantasy.ToolResponse, bool) {
	if sa.PermissionMode != subagents.PermissionModeBypassPermissions || subagents.InGlobalDir(sa.FilePath) {
		return fantasy.ToolResponse{}, true
	}
	granted, err := c.permissions.Request(ctx, permission.CreatePermissionRequest{
		SessionID:   sessionID,
		ToolCallID:  toolCallID,
		ToolName:    AgentToolName,
		Description: fmt.Sprintf("Subagent %q is defined in this project and requests bypassPermissions: it would run with every tool call auto-approved.", sa.Name),
		Action:      "bypass_permissions:" + sa.Name,
		Path:        sa.FilePath,
	})
	if err != nil || !granted {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("subagent %q requests bypassPermissions and the user did not approve it", sa.Name)), false
	}
	return fantasy.ToolResponse{}, true
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

	typeDesc := `The type of agent to use.
- "task": general read-only search and research, on the large model. Use when the question is open-ended and needs judgment.
- "fast": the same read-only tools on the small model. Use for one narrow lookup, and dispatch many in the same message — it is cheap enough that splitting a survey across several of them beats doing it yourself.`
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
				return fantasy.ToolResponse{}, errors.New("session id missing from context")
			}
			agentMessageID := tools.GetMessageFromContext(ctx)
			if agentMessageID == "" {
				return fantasy.ToolResponse{}, errors.New("agent message id missing from context")
			}

			// Every dispatch below runs a whole child session, so the
			// concurrency slot is taken here — before any build work — and
			// held for the run. Over the limit this blocks rather than
			// failing, so a wide fan-out completes in waves.
			release, slotErr := c.acquireDispatchSlot(ctx)
			if slotErr != nil {
				return fantasy.ToolResponse{}, slotErr
			}
			defer release()

			subagentType := params.SubagentType
			if subagentType == "" {
				subagentType = config.AgentTask
			}
			if builtin, ok := builtins[subagentType]; ok {
				builtAgent, err := builtin.get(ctx)
				if err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("build %s agent: %v", subagentType, err)), nil
				}
				return c.runSubAgent(ctx, subAgentParams{
					Agent:          builtAgent,
					SessionID:      sessionID,
					AgentMessageID: agentMessageID,
					ToolCallID:     call.ID,
					Prompt:         params.Prompt,
					SessionTitle:   "New Agent Session",
					AgentName:      subagentType,
					AgentColor:     subagents.AutoColor(subagentType),
					AgentModel:     builtAgent.Model().ModelCfg.Model,
				})
			}

			sa := findSubagentByName(c.activeSubagentsList(), subagentType)
			if sa == nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("unknown subagent type: %q", subagentType)), nil
			}

			if resp, ok := c.confirmBypassPermissions(ctx, sa, sessionID, call.ID); !ok {
				return resp, nil
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

			return c.runSubAgent(ctx, subAgentParams{
				Agent:          agent,
				SessionID:      sessionID,
				AgentMessageID: agentMessageID,
				ToolCallID:     call.ID,
				Prompt:         params.Prompt,
				SessionTitle:   sa.Name + " Agent Session",
				SessionSetup:   c.subagentSessionSetup(sa),
				AgentName:      sa.Name,
				AgentColor:     sa.ResolvedColor(),
				AgentModel:     agent.Model().ModelCfg.Model,
			})
		},
	}, nil
}
