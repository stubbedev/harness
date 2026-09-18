package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"charm.land/fantasy"
	"github.com/stubbedev/harness/internal/hooks"
)

// Gate fires a hook event for a privileged host function, so an
// extension reaching for the filesystem, the shell or the network is
// visible to the same PreToolUse policies that police tool calls.
//
// The gate must not itself dispatch back into extensions: an extension
// is single-threaded, and a handler that re-entered its own VM would
// deadlock. Callers pass a hook registry with no extension dispatcher
// attached; dispatchGuard defends the invariant regardless.
type Gate interface {
	Has(event string) bool
	Run(ctx context.Context, ec hooks.EventContext) (hooks.AggregateResult, error)
}

// Options configures a Host.
type Options struct {
	// Paths are the directories scanned for extension directories.
	Paths []string
	// Disabled lists extension names to skip.
	Disabled []string
	// WorkingDir is the workspace root. Relative paths an extension uses
	// resolve against it, and commands run in it by default.
	WorkingDir string
	// DataDir is the workspace's data directory, exposed to extensions
	// as a place to keep their own state.
	DataDir string
	// Gate polices privileged host functions. Optional: without one,
	// host functions run ungated.
	Gate Gate
	// HTTPClient is used by harness.http. Optional.
	HTTPClient *http.Client
	// CallTimeout bounds one call into an extension. Zero means
	// DefaultCallTimeout.
	CallTimeout time.Duration
	// SessionFromContext extracts the session ID a call belongs to, for
	// the hook payloads the gate builds. Optional.
	SessionFromContext func(context.Context) string
	// MaxConcurrentJobs bounds background jobs running at once across
	// every extension. Zero means defaultMaxConcurrentJobs.
	MaxConcurrentJobs int
}

// Host owns every loaded extension in a workspace: their VMs, and the
// tools, commands and hook handlers they registered. A nil *Host is
// valid and inert, so callers can hold one without nil checks.
type Host struct {
	opts Options

	mu        sync.RWMutex
	instances []*instance
	states    []*State

	httpOnce sync.Once
	client   *http.Client

	// jobs owns background jobs: the runs, and the queue their results
	// wait in until something collects them.
	jobs *jobRunner

	// jobCtx is the lifetime every background job is bound to. It is
	// detached from the call that starts a job -- outliving that call is
	// what a job is for -- and cancelled by Close.
	jobCtx    context.Context
	jobCancel context.CancelFunc
}

// New discovers the extensions in opts.Paths and loads each one. Loading
// runs init.lua, which is where an extension registers what it provides.
// An extension that fails to load is recorded in States and skipped; one
// bad extension never stops the others.
func New(ctx context.Context, opts Options) *Host {
	h := &Host{opts: opts}
	h.jobCtx, h.jobCancel = context.WithCancel(context.WithoutCancel(ctx))
	h.jobs = newJobRunner(h, opts.MaxConcurrentJobs)
	if len(opts.Paths) == 0 {
		return h
	}

	found, states := Discover(opts.Paths)
	enabled, disabledStates := Filter(found, opts.Disabled)
	states = append(states, disabledStates...)

	for _, ext := range enabled {
		in, err := spawnLoadedInstance(ctx, h, ext)
		if err != nil {
			slog.Warn("Failed to load extension", "extension", ext.Name, "path", ext.EntryFile, "error", err)
			states = append(states, &State{Name: ext.Name, Path: ext.EntryFile, State: StateError, Err: err})
			continue
		}
		slog.Debug(
			"Loaded extension",
			"extension", ext.Name,
			"tools", len(in.tools),
			"commands", len(in.commands),
			"hooks", len(in.hooks),
		)
		h.instances = append(h.instances, in)
		states = append(states, &State{Name: ext.Name, Path: ext.EntryFile, State: StateNormal})
	}

	slices.SortStableFunc(states, func(a, b *State) int {
		return strings.Compare(strings.ToLower(a.Path), strings.ToLower(b.Path))
	})
	h.states = states
	return h
}

// Close tears down every VM.
func (h *Host) Close() {
	if h == nil {
		return
	}
	if h.jobs != nil {
		h.jobs.cancelAll()
	}
	if h.jobCancel != nil {
		h.jobCancel()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, in := range h.instances {
		in.close()
	}
	h.instances = nil
}

// States returns the per-extension load outcome, for diagnostics.
func (h *Host) States() []*State {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return slices.Clone(h.states)
}

// Loaded returns the names of the extensions that loaded successfully.
func (h *Host) Loaded() []string {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	names := make([]string, 0, len(h.instances))
	for _, in := range h.instances {
		names = append(names, in.ext.Name)
	}
	return names
}

// Tools returns every tool the loaded extensions registered, as agent
// tools. Two extensions registering the same tool name is a conflict
// the second one loses: the tool list the model sees must be unique, and
// silently shadowing a name would make the winner arbitrary.
func (h *Host) Tools() []fantasy.AgentTool {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()

	var (
		out      []fantasy.AgentTool
		seen     = map[string]string{}
		jobsSeen bool
	)
	for _, in := range h.instances {
		jobsSeen = jobsSeen || len(in.jobSpecs) > 0
		for _, spec := range in.tools {
			if owner, taken := seen[spec.name]; taken {
				slog.Warn(
					"Extension tool name already registered; skipping",
					"tool", spec.name,
					"extension", in.ext.Name,
					"registered_by", owner,
				)
				continue
			}
			seen[spec.name] = in.ext.Name
			out = append(out, &luaTool{spec: spec})
		}
	}
	// The job tool is only worth a slot in the context when something
	// can actually produce a job.
	if jobsSeen {
		out = append(out, &jobTool{host: h})
	}
	slices.SortFunc(out, func(a, b fantasy.AgentTool) int {
		return strings.Compare(a.Info().Name, b.Info().Name)
	})
	return out
}

// Command is a slash command an extension registered.
type Command struct {
	// ID is the palette identifier, "ext:<extension>:<command>".
	ID string `json:"id"`
	// Extension is the extension that registered the command.
	Extension string `json:"extension"`
	// Name is the command's own name.
	Name string `json:"name"`
	// Description is shown in the command palette.
	Description string `json:"description,omitempty"`
	// Arguments are the values the command asks for before it runs.
	Arguments []Argument `json:"arguments,omitempty"`
}

// CommandPrefix marks a command that comes from an extension.
const CommandPrefix = "ext:"

// commandID builds the palette identifier for a command.
func commandID(extension, name string) string {
	return CommandPrefix + extension + ":" + name
}

// Commands returns every command the loaded extensions registered.
func (h *Host) Commands() []Command {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()

	var out []Command
	for _, in := range h.instances {
		for _, spec := range in.commands {
			out = append(out, Command{
				ID:          commandID(in.ext.Name, spec.name),
				Extension:   in.ext.Name,
				Name:        spec.name,
				Description: spec.description,
				Arguments:   spec.arguments,
			})
		}
	}
	slices.SortFunc(out, func(a, b Command) int {
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// RunCommand expands a command into the prompt it stands for. A command
// declared with a static prompt substitutes its arguments; one declared
// with a handler calls it with the arguments as a table.
func (h *Host) RunCommand(ctx context.Context, id string, args map[string]string) (string, error) {
	if h == nil {
		return "", fmt.Errorf("no extensions are loaded")
	}
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, in := range h.instances {
		for _, spec := range in.commands {
			if commandID(in.ext.Name, spec.name) != id && spec.name != id {
				continue
			}
			if spec.fn == nil {
				return substituteArgs(spec.prompt, args), nil
			}
			value, err := in.call(ctx, spec.fn, toLua(in.L, argsToAny(args)))
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(value.String()), nil
		}
	}
	return "", fmt.Errorf("extension command %q not found", id)
}

// substituteArgs replaces $NAME placeholders in a static prompt.
func substituteArgs(prompt string, args map[string]string) string {
	for name, value := range args {
		prompt = strings.ReplaceAll(prompt, "$"+name, value)
	}
	return prompt
}

func argsToAny(args map[string]string) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	return out
}

// callTimeout returns the configured per-call timeout.
func (h *Host) callTimeout() time.Duration {
	if h == nil || h.opts.CallTimeout <= 0 {
		return DefaultCallTimeout
	}
	return h.opts.CallTimeout
}

// httpClient returns the client harness.http uses, built once.
func (h *Host) httpClient() *http.Client {
	if h.opts.HTTPClient != nil {
		return h.opts.HTTPClient
	}
	h.httpOnce.Do(func() {
		h.client = &http.Client{Timeout: 30 * time.Second}
	})
	return h.client
}

// dispatchGuardKey marks a context that is already inside the extension
// system, so a hook fired from a host function can never dispatch back
// into a VM that is mid-call.
type dispatchGuardKey struct{}

func withDispatchGuard(ctx context.Context) context.Context {
	return context.WithValue(ctx, dispatchGuardKey{}, true)
}

func guarded(ctx context.Context) bool {
	value, _ := ctx.Value(dispatchGuardKey{}).(bool)
	return value
}

// gate fires a PreToolUse hook for a privileged host function and turns
// a denial into an error the script sees. Without a gate, or with no
// PreToolUse hooks configured, it is a no-op.
func (in *instance) gate(toolName string, input map[string]any) error {
	gate := in.host.opts.Gate
	if gate == nil || !gate.Has(hooks.EventPreToolUse) {
		return nil
	}

	ctx := withDispatchGuard(in.callContext())
	payload, err := json.Marshal(input)
	if err != nil {
		payload = []byte("{}")
	}

	var sessionID string
	if fn := in.host.opts.SessionFromContext; fn != nil {
		sessionID = fn(ctx)
	}

	result, err := gate.Run(ctx, hooks.EventContext{
		Event:     hooks.EventPreToolUse,
		SessionID: sessionID,
		CWD:       in.host.opts.WorkingDir,
		ToolName:  toolName,
		ToolInput: string(payload),
	})
	if err != nil {
		return err
	}
	if result.Decision == hooks.DecisionDeny {
		reason := result.Reason
		if reason == "" {
			reason = "blocked by hook"
		}
		return fmt.Errorf("%s denied: %s", toolName, reason)
	}
	return nil
}

// Info summarises one extension for diagnostics: what it registered, or
// why it is not there.
type Info struct {
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	State    string   `json:"state"`
	Error    string   `json:"error,omitempty"`
	Tools    []string `json:"tools,omitempty"`
	Commands []string `json:"commands,omitempty"`
	Events   []string `json:"events,omitempty"`
}

// Describe reports every extension the host knows about, loaded or not,
// in path order. It is what the harness self-inspection tool renders.
func (h *Host) Describe() []Info {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()

	loaded := make(map[string]*instance, len(h.instances))
	for _, in := range h.instances {
		loaded[in.ext.Name] = in
	}

	infos := make([]Info, 0, len(h.states))
	for _, state := range h.states {
		info := Info{Name: state.Name, Path: state.Path, State: stateName(state.State)}
		if state.Err != nil {
			info.Error = state.Err.Error()
		}
		if in, ok := loaded[state.Name]; ok {
			for _, tool := range in.tools {
				info.Tools = append(info.Tools, tool.name)
			}
			for _, cmd := range in.commands {
				info.Commands = append(info.Commands, commandID(in.ext.Name, cmd.name))
			}
			for event, handlers := range in.hooks {
				if len(handlers) > 0 {
					info.Events = append(info.Events, event)
				}
			}
			slices.Sort(info.Events)
		}
		infos = append(infos, info)
	}
	return infos
}

// stateName renders a discovery state for display.
func stateName(state DiscoveryState) string {
	switch state {
	case StateError:
		return "error"
	case StateDisabled:
		return "disabled"
	default:
		return "loaded"
	}
}

// jobContext returns the lifetime background jobs are bound to.
func (h *Host) jobContext() context.Context {
	if h.jobCtx == nil {
		return context.Background()
	}
	return h.jobCtx
}

// Jobs returns a snapshot of every background job, oldest first.
func (h *Host) Jobs() []Job {
	if h == nil || h.jobs == nil {
		return nil
	}
	return h.jobs.list()
}
