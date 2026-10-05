// Package app wires together services, coordinates agents, and manages
// application lifecycle.
package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/fantasy"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/stubbedev/harness/internal/agent"
	"github.com/stubbedev/harness/internal/agent/notify"
	"github.com/stubbedev/harness/internal/agent/tools"
	"github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/agentstate"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/checkpoints"
	"github.com/stubbedev/harness/internal/config"
	"github.com/stubbedev/harness/internal/crash"
	"github.com/stubbedev/harness/internal/db"
	"github.com/stubbedev/harness/internal/extensions"
	"github.com/stubbedev/harness/internal/filetracker"
	"github.com/stubbedev/harness/internal/herdr"
	"github.com/stubbedev/harness/internal/history"
	"github.com/stubbedev/harness/internal/lsp"
	"github.com/stubbedev/harness/internal/memory"
	"github.com/stubbedev/harness/internal/message"
	"github.com/stubbedev/harness/internal/presence"
	"github.com/stubbedev/harness/internal/pubsub"
	"github.com/stubbedev/harness/internal/question"
	"github.com/stubbedev/harness/internal/session"
	"github.com/stubbedev/harness/internal/skills"
	"github.com/stubbedev/harness/internal/subagents"
	"github.com/stubbedev/harness/internal/tmux"
	"github.com/stubbedev/harness/internal/update"
	"github.com/stubbedev/harness/internal/version"
)

// UpdateAvailableMsg is sent when a new version is available.
type UpdateAvailableMsg struct {
	CurrentVersion string
	LatestVersion  string
	IsDevelopment  bool
}

type App struct {
	Sessions    session.Service
	Messages    message.Service
	History     history.Service
	Questions   question.Service
	FileTracker filetracker.Service
	Memory      memory.Service
	// Checkpoints snapshots the working tree at each user turn so a
	// session can be rewound. Nil-safe: a nil service disables the
	// feature.
	Checkpoints *checkpoints.Service
	// Presence publishes this instance in the workspace's cross-process
	// registry so concurrent harness instances can discover each other.
	// Nil-safe: a failed registry disables discovery, nothing else.
	Presence *presence.Registry

	AgentCoordinator agent.Coordinator

	LSPManager *lsp.Manager

	Skills          *skills.Manager
	Extensions      *extensions.Host
	Subagents       *subagents.Manager
	SubagentRuntime *subagents.Runtime

	config *config.ConfigStore

	serviceEventsWG *sync.WaitGroup
	eventsCtx       context.Context
	events          *pubsub.Broker[tea.Msg]
	tuiWG           *sync.WaitGroup

	// global context and cleanup functions
	globalCtx          context.Context
	cleanupFuncs       []func(context.Context) error
	agentNotifications *pubsub.Broker[notify.Notification]
	// runCompletions is the authoritative per-run completion signal,
	// emitted once per top-level agent turn after all message
	// updates have been flushed. Bridged into app.events so SSE
	// subscribers (notably `harness run` in client/server mode) can
	// drive their exit on a deterministic, payload-bearing event
	// instead of guessing from message finish parts.
	runCompletions *pubsub.Broker[notify.RunComplete]

	// herdrClient and tmuxClient report agent state to the surrounding
	// terminal multiplexer when Harness runs inside one of their panes.
	// Nil outside their environments.
	herdrClient *herdr.Client
	tmuxClient  *tmux.Client
}

// newPresence creates and starts the workspace presence registry. A
// failure only disables cross-instance discovery; it never blocks the
// app from coming up.
func newPresence(ctx context.Context, dataDir string) *presence.Registry {
	registry, err := presence.New(dataDir)
	if err != nil {
		slog.Warn("Failed to create presence registry", "error", err)
		return nil
	}
	registry.Start(ctx)
	return registry
}

// New initializes a new application instance. skillsMgr carries the
// per-workspace skill discovery results computed by the caller; the
// caller is responsible for constructing it (typically via
// skills.NewManager + skills.DiscoverFromConfig). subagentsMgr carries
// the per-workspace subagent discovery results; may be nil.
func New(ctx context.Context, conn *sql.DB, store *config.ConfigStore, skillsMgr *skills.Manager, subagentsMgr *subagents.Manager) (*App, error) {
	q := db.New(conn)
	sessions := session.NewService(q, conn)
	messages := message.NewService(q)
	files := history.NewService(q)
	// Memory reads its reap limit lazily so live config reloads of
	// options.memory.max_memories are honored without rebuilding.
	memories := memory.NewService(q, conn, memory.WithReapLimit(func() int {
		if cfg := store.Config(); cfg != nil && cfg.Options != nil {
			return cfg.Options.Memory.GetMaxMemories()
		}
		return config.DefaultMaxMemories
	}))
	cfg := store.Config()
	app := &App{
		Sessions:    sessions,
		Messages:    messages,
		History:     files,
		Questions:   question.NewService(),
		FileTracker: filetracker.NewService(q, filetracker.WithBaseDir(store.WorkingDir())),
		Memory:      memories,
		Checkpoints: checkpoints.NewService(
			q,
			store.WorkingDir(),
			cfg.Options.DataDirectory,
			messages,
			sessions,
		),
		LSPManager: lsp.NewManager(store),
		Skills:     skillsMgr,
		Extensions: extensions.New(ctx, extensionOptions(store)),
		Subagents:  subagentsMgr,
		Presence:   newPresence(ctx, cfg.Options.DataDirectory),

		// Created eagerly (rather than lazily in initCoderAgent) so
		// Subscribe's one-time nil check always finds a live Runtime: on an
		// unconfigured install, New returns before InitCoderAgent runs, and
		// Subscribe (already running by the time the first provider gets
		// connected and InitCoderAgent runs for the first time) would
		// otherwise never wire up the subagent-events forwarding goroutine
		// for the rest of the process.
		SubagentRuntime: subagents.NewRuntime(),

		globalCtx: ctx,

		config: store,

		events:             pubsub.NewStreamingBroker[tea.Msg](),
		serviceEventsWG:    &sync.WaitGroup{},
		tuiWG:              &sync.WaitGroup{},
		agentNotifications: pubsub.NewBroker[notify.Notification](),
		runCompletions:     pubsub.NewBroker[notify.RunComplete](),
	}

	app.setupEvents()

	// Clipboard support initializes lazily: every entry point re-runs
	// the idempotent Init (see internal/clipboard), so touching
	// X11/Wayland here would only add startup latency, worst on
	// headless boxes where the probe fails.

	// Check for updates in the background, unless the binary is managed
	// externally (nix, package manager) and the check was disabled.
	if !app.config.Config().Options.DisableUpdateCheck {
		crash.Go("app.checkForUpdates", func() { app.checkForUpdates(ctx) })
	}

	// Arm initialization synchronously before launching it so WaitForInit
	// blocks for the in-flight init instead of racing the goroutine and
	// returning before any MCP tools register. Elicitation must be wired
	// first: Initialize advertises the capability only when a handler is
	// installed.
	wireMCPElicitation(app.Questions)
	mcp.ArmInit()
	crash.Go("mcp.Initialize", func() { mcp.Initialize(ctx, store) })

	// Start multiplexer integrations when running inside a herdr or
	// tmux pane. One bridge feeds every reporter: the shared event
	// translation is single-sourced in internal/agentstate.
	app.herdrClient = herdr.Init()
	app.tmuxClient = tmux.Init()
	agentstate.BridgeLocal(ctx, func(ev agentstate.Event) {
		app.herdrClient.HandleEvent(ev)
		app.tmuxClient.HandleEvent(ev)
	}, agentstate.BridgeSources{
		RunCompletions: app.runCompletions,
		Messages:       app.Messages,
	})

	// Release the shared database connection on shutdown. The pool
	// closes the underlying *sql.DB when the last reference is released.
	dataDir := cfg.Options.DataDirectory
	app.cleanupFuncs = append(
		app.cleanupFuncs,
		func(context.Context) error { return db.Release(dataDir) },
		func(ctx context.Context) error { return mcp.Close(ctx) },
		func(context.Context) error { app.Extensions.Close(); return nil },
	)

	// TODO: remove the concept of agent config, most likely.
	if !cfg.IsConfigured() {
		slog.Warn("No agent configuration found")
		return app, nil
	}
	if err := app.InitCoderAgent(ctx); err != nil {
		return nil, fmt.Errorf("failed to initialize coder agent: %w", err)
	}

	// Set up callback for LSP state updates.
	app.LSPManager.SetCallback(func(name string, client *lsp.Client) {
		if client == nil {
			updateLSPState(name, lsp.StateUnstarted, nil, nil, 0)
			return
		}
		client.SetDiagnosticsCallback(updateLSPDiagnostics)
		updateLSPState(name, client.GetServerState(), nil, client, 0)
	})

	// TrackConfigured must run after SetCallback so the callback is already
	// installed when configured-but-not-yet-started LSPs are announced.
	crash.Go("lsp.TrackConfigured", func() { app.LSPManager.TrackConfigured(ctx) })

	return app, nil
}

// Config returns the pure-data configuration.
func (app *App) Config() *config.Config {
	return app.config.Config()
}

// Store returns the config store.
func (app *App) Store() *config.ConfigStore {
	return app.config
}

// Events returns a per-caller subscription channel for application events.
// Each caller receives its own channel; all callers receive every event.
func (app *App) Events(ctx context.Context) <-chan pubsub.Event[tea.Msg] {
	return app.events.Subscribe(ctx)
}

// SendEvent publishes a message to all event subscribers.
func (app *App) SendEvent(msg tea.Msg) {
	app.events.Publish(pubsub.UpdatedEvent, msg)
}

// AgentNotifications returns the broker for agent notification events.
func (app *App) AgentNotifications() *pubsub.Broker[notify.Notification] {
	return app.agentNotifications
}

// RunCompletions returns the broker for the authoritative per-run
// terminal RunComplete events. The dispatcher (backend.runAgent) uses
// it to emit a reliable terminal event when a run fails before the
// coordinator could publish one of its own.
func (app *App) RunCompletions() *pubsub.Broker[notify.RunComplete] {
	return app.runCompletions
}

// ReportCurrentSession tells herdr which session the user is now
// viewing so it can persist a resumable reference for the pane. Safe
// to call when not running inside a herdr pane; the underlying client
// is nil-safe. Call this whenever the active session changes (load,
// new, or select).
func (app *App) ReportCurrentSession(sessionID string) {
	app.herdrClient.SetSessionID(sessionID)
	app.tmuxClient.SetSessionID(sessionID)
}

// resolveSession resolves which session to use for a non-interactive run
// If continueSessionID is set, it looks up that session by ID
// If useLast is set, it returns the most recently updated top-level session
// Otherwise, it creates a new session
func (app *App) resolveSession(ctx context.Context, continueSessionID string, useLast bool) (session.Session, error) {
	switch {
	case continueSessionID != "":
		if session.IsAgentToolSession(continueSessionID) {
			return session.Session{}, fmt.Errorf("cannot continue an agent tool session: %s", continueSessionID)
		}
		sess, err := app.Sessions.Get(ctx, continueSessionID)
		if err != nil {
			return session.Session{}, fmt.Errorf("session not found: %s", continueSessionID)
		}
		if sess.ParentSessionID != "" {
			return session.Session{}, fmt.Errorf("cannot continue a child session: %s", continueSessionID)
		}
		return sess, nil

	case useLast:
		sess, err := app.Sessions.GetLast(ctx)
		if err != nil {
			return session.Session{}, fmt.Errorf("no sessions found to continue")
		}
		return sess, nil

	default:
		return app.Sessions.Create(ctx, agent.DefaultSessionName)
	}
}

// NonInteractiveOptions configures RunNonInteractive.
type NonInteractiveOptions struct {
	// Output receives the assistant's text as it streams.
	Output io.Writer
	Prompt string
	// LargeModel and SmallModel override the configured models for this
	// run; empty keeps them.
	LargeModel string
	SmallModel string
	// ReasoningEffort overrides the large model's effort for this run.
	ReasoningEffort string
	// ContinueSessionID resumes that session; UseLast resumes the most
	// recent one. Neither starts a new session.
	ContinueSessionID string
	UseLast           bool
	// NewSpinner, when set, starts a progress indicator on a terminal
	// stderr; cancel ends the run, for an indicator that takes ctrl+c.
	// The app stops it once output begins. Rendering it is the caller's
	// business, so this package draws no UI of its own.
	NewSpinner func(ctx context.Context, cancel context.CancelFunc) Spinner
}

// Spinner is a progress indicator RunNonInteractive stops once output
// begins.
type Spinner interface {
	Stop()
}

// RunNonInteractive runs the application in non-interactive mode with the
// given prompt, printing to opts.Output.
func (app *App) RunNonInteractive(ctx context.Context, opts NonInteractiveOptions) error {
	slog.Info("Running in non-interactive mode")
	output, prompt := opts.Output, opts.Prompt
	largeModel, smallModel, reasoningEffort := opts.LargeModel, opts.SmallModel, opts.ReasoningEffort
	continueSessionID, useLast := opts.ContinueSessionID, opts.UseLast

	// Re-initialize the coder agent without interactive-only tools.
	if err := app.InitCoderAgentNonInteractive(ctx); err != nil {
		return fmt.Errorf("failed to reinitialize agent for non-interactive mode: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if largeModel != "" || smallModel != "" {
		if err := app.overrideModelsForNonInteractive(ctx, largeModel, smallModel); err != nil {
			return fmt.Errorf("failed to override models: %w", err)
		}
	}

	// The reasoning effort applies to the model that will actually run.
	// On a continued session without an explicit model override, the
	// model is resolved later from the session's last assistant message,
	// so the override is applied after that restore instead.
	deferredEffort := (continueSessionID != "" || useLast) && largeModel == "" && smallModel == ""
	if reasoningEffort != "" && !deferredEffort {
		if err := app.overrideReasoningEffort(ctx, reasoningEffort); err != nil {
			return err
		}
	}

	fmt.Fprintln(os.Stderr, app.config.Config().ResolvedLargeLine())

	var (
		spinner   Spinner
		stderrTTY bool
		progress  bool
	)

	stderrTTY = term.IsTerminal(os.Stderr.Fd())
	progress = app.config.Config().Options.ProgressEnabled()

	if opts.NewSpinner != nil && stderrTTY {
		spinner = opts.NewSpinner(ctx, cancel)
	}

	// Helper function to stop spinner once.
	stopSpinner := func() {
		if spinner != nil {
			spinner.Stop()
			spinner = nil
		}
	}

	// Non-interactive runs get a single shot at the tool palette, so wait for
	// MCP initialization to settle before reading MCP tools. The coordinator
	// waits again for the same reason (it is the gate the client/server path
	// goes through); doing it here too surfaces the failure before we create a
	// session, and lets the UpdateModels below see every MCP tool. The wait is
	// bounded so a server wedged mid-handshake cannot stall the one-shot run
	// for its full connect timeout; past the budget the run proceeds without
	// the unfinished servers' tools.
	if err := mcp.WaitForInitBudget(ctx, mcp.InitWaitBudget); err != nil {
		return fmt.Errorf("failed to wait for MCP initialization: %w", err)
	}

	// force update of agent models before running so mcp tools are loaded
	app.AgentCoordinator.UpdateModels(ctx)

	defer stopSpinner()

	sess, err := app.resolveSession(ctx, continueSessionID, useLast)
	if err != nil {
		return fmt.Errorf("failed to create session for non-interactive mode: %w", err)
	}

	if continueSessionID != "" || useLast {
		slog.Info("Continuing session for non-interactive run", "session_id", sess.ID)
		// If no explicit model override was requested, restore the
		// model/provider from the last assistant message in the
		// session, provided it is still available.
		if largeModel == "" && smallModel == "" {
			if err := app.restoreModelFromSession(ctx, sess.ID); err != nil {
				slog.Warn("Failed to restore model from session", "error", err)
			}
		}
	} else {
		slog.Info("Created session for non-interactive run", "session_id", sess.ID)
	}

	if reasoningEffort != "" && deferredEffort {
		if err := app.overrideReasoningEffort(ctx, reasoningEffort); err != nil {
			return err
		}
	}

	// Report session identity to herdr.
	app.ReportCurrentSession(sess.ID)

	type response struct {
		result *fantasy.AgentResult
		err    error
	}
	done := make(chan response, 1)

	// Subscribe before the run starts: a subscription opened after it
	// misses whatever the run publishes first.
	messageEvents := app.Messages.Subscribe(ctx)
	messageReadBytes := make(map[string]int)
	var printed bool

	go func(ctx context.Context, sessionID, prompt string) {
		// A panic in the run must still unblock the caller below, which
		// waits on done with no timeout of its own.
		defer crash.Recover("app.cliAgentRun", func() {
			done <- response{err: errors.New("agent run panicked; see the crash report")}
		})
		result, err := app.AgentCoordinator.Run(ctx, sessionID, prompt)
		if err != nil {
			done <- response{
				err: fmt.Errorf("failed to start agent processing stream: %w", err),
			}
			return
		}
		done <- response{
			result: result,
		}
	}(ctx, sess.ID, prompt)

	// printDelta writes the part of an assistant message not yet printed.
	printDelta := func(msg message.Message) error {
		if msg.SessionID != sess.ID || msg.Role != message.Assistant || len(msg.Parts) == 0 {
			return nil
		}
		stopSpinner()

		content := msg.Content().String()
		readBytes := messageReadBytes[msg.ID]

		if len(content) < readBytes {
			slog.Error("Non-interactive: message content is shorter than read bytes", "message_length", len(content), "read_bytes", readBytes)
			return fmt.Errorf("message content is shorter than read bytes: %d < %d", len(content), readBytes)
		}

		part := content[readBytes:]
		// Trim leading whitespace. Sometimes the LLM includes leading
		// formatting and intentation, which we don't want here.
		if readBytes == 0 {
			part = strings.TrimLeft(part, " \t")
		}
		// Ignore initial whitespace-only messages.
		if printed || strings.TrimSpace(part) != "" {
			printed = true
			fmt.Fprint(output, part)
		}
		messageReadBytes[msg.ID] = len(content)
		return nil
	}

	defer func() {
		if progress && stderrTTY {
			_, _ = fmt.Fprintf(os.Stderr, ansi.ResetProgressBar)
		}

		// Always print a newline at the end. If output is a TTY this will
		// prevent the prompt from overwriting the last line of output.
		_, _ = fmt.Fprintln(output)
	}()

	for {
		if progress && stderrTTY {
			// HACK: Reinitialize the terminal progress bar on every iteration
			// so it doesn't get hidden by the terminal due to inactivity.
			_, _ = fmt.Fprintf(os.Stderr, ansi.SetIndeterminateProgressBar)
		}

		select {
		case result := <-done:
			stopSpinner()
			// The run's last updates can still sit in the subscription
			// buffer: select picks among ready cases at random, so done
			// can win over them. Print them before returning.
			for drained := false; !drained; {
				select {
				case event, ok := <-messageEvents:
					if !ok {
						drained = true
						break
					}
					if err := printDelta(event.Payload); err != nil {
						return err
					}
				default:
					drained = true
				}
			}
			if result.err != nil {
				if errors.Is(result.err, context.Canceled) || errors.Is(result.err, agent.ErrRequestCancelled) {
					slog.Debug("Non-interactive: agent processing cancelled", "session_id", sess.ID)
					return nil
				}
				return fmt.Errorf("agent processing failed: %w", result.err)
			}
			return nil

		case event := <-messageEvents:
			if err := printDelta(event.Payload); err != nil {
				return err
			}

		case <-ctx.Done():
			stopSpinner()
			return ctx.Err()
		}
	}
}

func (app *App) UpdateAgentModel(ctx context.Context) error {
	if app.AgentCoordinator == nil {
		return fmt.Errorf("agent configuration is missing")
	}
	return app.AgentCoordinator.UpdateModels(ctx)
}

// restoreModelFromSession reads the last assistant message in the
// session and, if it used a different provider/model than the current
// config, overrides the preferred model in-memory (non-persistent)
// provided the provider/model is still available. This ensures that
// continuing a session uses the same model that produced the last
// response.
func (app *App) restoreModelFromSession(ctx context.Context, sessionID string) error {
	lastMsg, err := app.Messages.GetLastAssistantMessage(ctx, sessionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("failed to get last assistant message: %w", err)
	}
	if lastMsg.Provider == "" || lastMsg.Model == "" {
		return nil
	}

	cfg := app.config.Config()
	currentLarge := cfg.Models[config.SelectedModelTypeLarge]
	if currentLarge.Provider == lastMsg.Provider && currentLarge.Model == lastMsg.Model {
		return nil
	}

	if !cfg.IsModelAvailable(lastMsg.Provider, lastMsg.Model) {
		slog.Debug("Skipping model restoration: provider/model not available",
			"provider", lastMsg.Provider,
			"model", lastMsg.Model)
		return nil
	}

	app.config.OverridePreferredModel(config.SelectedModelTypeLarge, config.SelectedModel{
		Provider: lastMsg.Provider,
		Model:    lastMsg.Model,
	})
	if _, ok := cfg.Models[config.SelectedModelTypeSmall]; !ok {
		smallModel := app.GetDefaultSmallModel(lastMsg.Provider)
		app.config.OverridePreferredModel(config.SelectedModelTypeSmall, smallModel)
	}
	if err := app.AgentCoordinator.UpdateModels(ctx); err != nil {
		return fmt.Errorf("failed to update agent models: %w", err)
	}
	slog.Info("Restored model from session",
		"provider", lastMsg.Provider,
		"model", lastMsg.Model)
	return nil
}

// overrideModelsForNonInteractive parses the model strings and temporarily
// overrides the model configurations, then rebuilds the agent.
// Format: "model-name" (searches all providers) or "provider/model-name".
// Model matching is case-insensitive.
// If largeModel is provided but smallModel is not, the small model defaults to
// the provider's default small model.
func (app *App) overrideModelsForNonInteractive(ctx context.Context, largeModel, smallModel string) error {
	providers := app.config.Config().Providers.Copy()

	largeMatches, smallMatches, err := FindModels(providers, largeModel, smallModel)
	if err != nil {
		return err
	}

	var largeProviderID string

	// Override large model.
	if largeModel != "" {
		found, err := ValidateModels(largeMatches, largeModel, "large")
		if err != nil {
			return err
		}
		largeProviderID = found.Provider
		slog.Info("Overriding large model for non-interactive run", "provider", found.Provider, "model", found.ModelID)
		app.config.OverridePreferredModel(config.SelectedModelTypeLarge, config.SelectedModel{
			Provider: found.Provider,
			Model:    found.ModelID,
		})
	}

	// Override small model.
	switch {
	case smallModel != "":
		found, err := ValidateModels(smallMatches, smallModel, "small")
		if err != nil {
			return err
		}
		slog.Info("Overriding small model for non-interactive run", "provider", found.Provider, "model", found.ModelID)
		app.config.OverridePreferredModel(config.SelectedModelTypeSmall, config.SelectedModel{
			Provider: found.Provider,
			Model:    found.ModelID,
		})

	case largeModel != "":
		// No small model specified, but large model was - use provider's default.
		smallCfg := app.GetDefaultSmallModel(largeProviderID)
		app.config.OverridePreferredModel(config.SelectedModelTypeSmall, smallCfg)
	}

	return app.AgentCoordinator.UpdateModels(ctx)
}

// overrideReasoningEffort validates the requested reasoning effort against
// the large model in effect for this run (which may have been overridden by
// --model or restored from a continued session) and applies it as an
// in-memory override.
func (app *App) overrideReasoningEffort(ctx context.Context, reasoningEffort string) error {
	cfg := app.config.Config()
	selected, ok := cfg.Models[config.SelectedModelTypeLarge]
	if !ok {
		return fmt.Errorf("no large model selected; set one with the --model flag or 'model large'")
	}
	if err := cfg.ValidateReasoningEffort(selected.Provider, selected.Model, reasoningEffort); err != nil {
		return err
	}
	selected.ReasoningEffort = reasoningEffort
	slog.Info("Overriding reasoning effort for non-interactive run",
		"provider", selected.Provider,
		"model", selected.Model,
		"reasoning_effort", reasoningEffort)
	app.config.OverridePreferredModel(config.SelectedModelTypeLarge, selected)
	return app.AgentCoordinator.UpdateModels(ctx)
}

// GetDefaultSmallModel returns the default small model for the given
// provider. Falls back to the large model if no default is found.
func (app *App) GetDefaultSmallModel(providerID string) config.SelectedModel {
	cfg := app.config.Config()
	largeModelCfg := cfg.Models[config.SelectedModelTypeLarge]

	// Find the provider in the known providers list to get its default small model.
	knownProviders, _ := config.Providers(cfg)
	if config.KnownProviderByID(knownProviders, providerID) == nil {
		// For unknown/local providers, use the large model as small.
		slog.Warn("Using large model as small model for unknown provider", "provider", providerID, "model", largeModelCfg.Model)
		return largeModelCfg
	}
	knownProvider := config.KnownProviderByID(knownProviders, providerID)
	defaultSmallModelID := knownProvider.DefaultSmallModelID
	model := cfg.GetModel(providerID, defaultSmallModelID)
	if model == nil {
		slog.Warn("Default small model not found, using large model", "provider", providerID, "model", largeModelCfg.Model)
		return largeModelCfg
	}

	slog.Info("Using provider default small model", "provider", providerID, "model", defaultSmallModelID)
	return config.SelectedModel{
		Provider:        providerID,
		Model:           defaultSmallModelID,
		MaxTokens:       model.DefaultMaxTokens,
		ReasoningEffort: catalog.DefaultReasoningLevel(model.ReasoningLevels),
	}
}

func (app *App) setupEvents() {
	ctx, cancel := context.WithCancel(app.globalCtx)
	app.eventsCtx = ctx
	app.subscribe(ctx, "sessions", app.Sessions.Subscribe)
	app.subscribe(ctx, "messages", app.Messages.Subscribe)
	app.subscribe(ctx, "question-batches", app.Questions.Subscribe)
	app.subscribe(ctx, "question-notifications", app.Questions.SubscribeNotifications)
	app.subscribe(ctx, "history", app.History.Subscribe)
	app.subscribe(ctx, "agent-notifications", app.agentNotifications.Subscribe)
	app.subscribe(ctx, "run-completions", app.runCompletions.Subscribe)
	app.subscribe(ctx, "mcp", mcp.SubscribeEvents)
	app.subscribe(ctx, "lsp", SubscribeLSPEvents)
	if app.Skills != nil {
		app.subscribe(ctx, "skills", app.Skills.SubscribeEvents)
	}
	cleanupFunc := func(context.Context) error {
		cancel()
		app.serviceEventsWG.Wait()
		app.events.Shutdown()
		return nil
	}
	app.cleanupFuncs = append(app.cleanupFuncs, cleanupFunc)
}

// subscribe fans a service's event stream into the shared app.events
// broker on app.serviceEventsWG, re-publishing each upstream event as a
// tea.Msg with the delivery guarantee it was published with. The goroutine exits when ctx is cancelled or the upstream
// channel closes. It is a generic method (Go 1.27) so it can live in the
// App namespace while still inferring the upstream event type T.
func (app *App) subscribe[T any](
	ctx context.Context,
	name string,
	subscriber func(context.Context) <-chan pubsub.Event[T],
) {
	app.serviceEventsWG.Go(func() {
		subCh := subscriber(ctx)
		for {
			select {
			case event, ok := <-subCh:
				if !ok {
					slog.Debug("Subscription channel closed", "name", name)
					return
				}
				// Forward with the guarantee the event was published with:
				// a terminal message update or a RunComplete must reach
				// the UI and the SSE stream, not just this goroutine.
				if event.MustDeliver {
					app.events.PublishMustDeliver(ctx, pubsub.UpdatedEvent, tea.Msg(event))
				} else {
					app.events.Publish(pubsub.UpdatedEvent, tea.Msg(event))
				}
			case <-ctx.Done():
				slog.Debug("Subscription cancelled", "name", name)
				return
			}
		}
	})
}

func (app *App) InitCoderAgent(ctx context.Context) error {
	return app.initCoderAgent(ctx, true)
}

// InitCoderAgentNonInteractive initializes the coder agent without
// interactive-only tools (e.g. question).
func (app *App) InitCoderAgentNonInteractive(ctx context.Context) error {
	return app.initCoderAgent(ctx, false)
}

func (app *App) initCoderAgent(ctx context.Context, interactive bool) error {
	coderAgentCfg := app.config.Config().Agents[config.AgentCoder]
	if coderAgentCfg.ID == "" {
		return fmt.Errorf("coder agent configuration is missing")
	}
	var err error
	app.AgentCoordinator, err = agent.NewCoordinator(ctx, agent.CoordinatorOptions{
		Config:       app.config,
		Sessions:     app.Sessions,
		Messages:     app.Messages,
		Checkpoints:  app.Checkpoints,
		Questions:    app.Questions,
		History:      app.History,
		FileTracker:  app.FileTracker,
		LSPManager:   app.LSPManager,
		Notify:       app.agentNotifications,
		RunComplete:  app.runCompletions,
		Skills:       app.Skills,
		Extensions:   app.Extensions,
		SubagentsMgr: app.Subagents,
		Runtime:      app.SubagentRuntime,
		Memory:       app.Memory,
		Presence:     app.Presence,
		Interactive:  interactive,
	})
	if err != nil {
		slog.Error("Failed to create coder agent", "err", err)
		return err
	}
	// Warm the readiness path off the submit path. Without this the
	// first prompt absorbs the background agent build (system prompt,
	// tool palette) plus the first model/tool rebuild, and until the
	// user message is persisted the editor looks dead: typed text sits
	// there and Enter appears to do nothing. Warming overlaps that work
	// with the user typing instead.
	warmupCtx := app.globalCtx
	crash.Go("agent.warmup", func() {
		if err := app.AgentCoordinator.Warmup(warmupCtx); err != nil {
			slog.Warn("Failed to warm up coder agent", "error", err)
		}
	})
	return nil
}

// Subscribe sends events to the TUI as tea.Msgs.
func (app *App) Subscribe(program *tea.Program) {
	defer crash.Recover("app.Subscribe", func() {
		slog.Info("TUI subscription panic: attempting graceful shutdown")
		program.Quit()
	})

	app.tuiWG.Add(1)
	tuiCtx, tuiCancel := context.WithCancel(app.globalCtx)
	app.cleanupFuncs = append(app.cleanupFuncs, func(context.Context) error {
		slog.Debug("Cancelling TUI message handler")
		tuiCancel()
		app.tuiWG.Wait()
		return nil
	})
	defer app.tuiWG.Done()

	if app.SubagentRuntime != nil {
		rtEvents := app.SubagentRuntime.Subscribe(tuiCtx)
		go func() {
			defer crash.Recover("app.subagentRuntimeEvents", nil)
			for ev := range rtEvents {
				program.Send(ev)
			}
		}()
	}

	if app.Subagents != nil {
		discEvents := app.Subagents.SubscribeEvents(tuiCtx)
		go func() {
			defer crash.Recover("app.subagentDiscoveryEvents", nil)
			for ev := range discEvents {
				program.Send(ev)
			}
		}()
	}

	events := app.events.Subscribe(tuiCtx)
	for {
		select {
		case <-tuiCtx.Done():
			slog.Debug("TUI message handler shutting down")
			return
		case ev, ok := <-events:
			if !ok {
				slog.Debug("TUI message channel closed")
				return
			}
			program.Send(ev.Payload)
		}
	}
}

// Shutdown performs a graceful shutdown of the application.
func (app *App) Shutdown() {
	start := time.Now()
	defer func() { slog.Debug("Shutdown took " + time.Since(start).String()) }()

	// Retire from the workspace presence registry first: no turn is
	// running anymore, and peers should see this instance leave. The
	// call is nil-safe; a failed registry retires nothing.
	app.Presence.Stop()

	// First, cancel all agents and wait for them to finish. This must complete
	// before closing the DB so agents can finish writing their state.
	if app.AgentCoordinator != nil {
		app.AgentCoordinator.CancelAll()
	}

	// Shared shutdown context for all timeout-bounded cleanup.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Drain any debounced message updates before the DB-close cleanup
	// runs in the parallel block below. message.Service buffers
	// streaming deltas (see internal/message/message.go) and we must
	// land them while the connection is still open.
	if app.Messages != nil {
		if err := app.Messages.FlushAll(shutdownCtx); err != nil {
			slog.Error("Failed to flush pending message updates on shutdown", "error", err)
		}
	}

	// Now run remaining cleanup tasks in parallel.
	var wg sync.WaitGroup

	// The agents' terminal sessions: real shells, each holding its
	// working directory open until it exits.
	wg.Go(tools.CloseTerminalSessions)

	// Close the multiplexer clients to release the pane and stop
	// their background writers.
	app.herdrClient.Close()
	app.tmuxClient.Close()

	// Release the subagent brokers and their subscriber goroutines. Agents were
	// cancelled above, so nothing is still publishing. Both are per-workspace,
	// and in server mode workspaces are created and torn down repeatedly for
	// the life of the process. Both calls tolerate a nil receiver.
	app.Subagents.Shutdown()
	app.SubagentRuntime.Shutdown()

	// Shutdown all LSP clients.
	wg.Go(func() {
		app.LSPManager.KillAll(shutdownCtx)
	})

	// Call all cleanup functions.
	for _, cleanup := range app.cleanupFuncs {
		if cleanup != nil {
			wg.Go(func() {
				if err := cleanup(shutdownCtx); err != nil {
					slog.Error("Failed to cleanup app properly on shutdown", "error", err)
				}
			})
		}
	}
	wg.Wait()
}

// checkForUpdates checks for available updates.
func (app *App) checkForUpdates(ctx context.Context) {
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	info, err := update.Check(checkCtx, version.Version, update.Default)
	if err != nil || !info.Available() {
		return
	}
	app.events.Publish(pubsub.UpdatedEvent, UpdateAvailableMsg{
		CurrentVersion: info.Current,
		LatestVersion:  info.Latest,
		IsDevelopment:  info.IsDevelopment(),
	})
}

// extensionOptions builds the extension host's options. The session
// lookup is wired here rather than inside the extensions package: it is
// the agent's context key, and keeping it out of that package is what
// lets extensions stay independent of the tool layer.
func extensionOptions(store *config.ConfigStore) extensions.Options {
	opts := extensions.OptionsFromStore(store)
	opts.SessionFromContext = tools.GetSessionFromContext
	return opts
}
