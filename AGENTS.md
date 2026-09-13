# Harness Development Guide

## Project Overview

Harness is a terminal-based AI coding assistant written in Go, forked from
[Crush](https://github.com/charmbracelet/crush) and maintained independently.
It connects to LLMs and gives them tools to read, write, and execute code. It
supports multiple providers (Anthropic, OpenAI, Gemini, Bedrock, Copilot,
MiniMax, Vercel, and more), integrates with LSPs for code intelligence, and
supports extensibility via MCP servers and agent skills.

The module path is `github.com/stubbedev/harness`.

## Architecture

```
main.go                            CLI entry point (cobra via internal/cmd)
internal/
  app/app.go                       Top-level wiring: DB, config, agents, LSP, MCP, events
  cmd/                             CLI commands (root, run, login, models, stats, sessions)
  config/
    config.go                      Config struct, context file paths, agent definitions
    load.go                        YAML config discovery, loading and validation
    yaml.go                        YAML <-> JSON conversion at the file boundary
    provider.go                    Provider configuration and model resolution
  agent/
    agent.go                       SessionAgent: runs LLM conversations per session
    coordinator.go                 Coordinator: manages named agents ("coder", "task")
    hooked_tool.go                 Decorator that runs Pre/PostToolUse hooks around tool execution
    prompts.go                     Loads Go-template system prompts
    templates/                     System prompt templates (coder.md.tpl, task.md.tpl, etc.)
    tools/                         All built-in tools (bash, edit, view, grep, glob, etc.)
      mcp/                         MCP client integration
  hooks/                           Hook engine: runs user shell commands on hook events
    hooks.go                       Decision types, aggregation logic, event constants
    registry.go                    Config-backed event registry; live-reloads, nil-safe
    runner.go                      Parallel hook execution, timeout, dedup
    input.go                       Stdin payload builder, env vars, stdout parsing (Harness + Claude Code compat)
  session/session.go               Session CRUD backed by SQLite
  message/                         Message model and content types
  memory/                          Durable agent memory: cross-session notes, secret scrubbing, index rendering
  db/                              SQLite via sqlc, with migrations
    sql/                           Raw SQL queries (consumed by sqlc)
    migrations/                    Schema migrations
  lsp/                             LSP client manager, auto-discovery, on-demand startup
  ui/                              Bubble Tea v2 TUI (see internal/ui/AGENTS.md)
  skills/                          Skill file discovery and loading
  shell/                           Bash command execution with background job support
  event/                           Telemetry (PostHog)
  pubsub/                          Internal pub/sub for cross-component messaging
  filetracker/                     Tracks files touched per session
  history/                         Prompt history
```

### Key Dependency Roles

- **`charm.land/fantasy`**: LLM provider abstraction layer. Handles protocol
  differences between Anthropic, OpenAI, Gemini, etc. Used in `internal/app`
  and `internal/agent`.
- **`charm.land/bubbletea/v2`**: TUI framework powering the interactive UI.
- **`charm.land/lipgloss/v2`**: Terminal styling.
- **`charm.land/glamour/v2`**: Markdown rendering in the terminal.
- **`charm.land/catwalk`**: Upstream catalog of models and providers.
- **`sqlc`**: Generates Go code from SQL queries in `internal/db/sql/`.

### Key Patterns

- **Vendored forks**: dependencies whose upstream is dormant and broken on a
  platform we build for are forked into `third_party/<module>` and pinned with
  a `replace` directive in `go.mod`. Each fork's README documents the patch
  and how to drop the fork once upstream fixes it.
- **Config is a Service**: accessed via `config.Service`, not global state.
- **Tools are self-documenting**: each tool has a `.go` implementation and a
  `.md` description file in `internal/agent/tools/`.
- **System prompts are Go templates**: `internal/agent/templates/*.md.tpl`
  with runtime data injected.
- **Context files**: Harness reads AGENTS.md, HARNESS.md, CLAUDE.md, GEMINI.md
  (and `.local` variants) from the working directory for project-specific
  instructions.
- **YAML config**: the only config format. Hand-written files are
  `$XDG_CONFIG_HOME/harness/config.yaml` and a project `harness.yaml` /
  `.harness.yaml`; Harness writes machine-owned state to `state.yaml` in the
  global and workspace data directories. Files are converted to JSON at the
  file boundary (`internal/config/yaml.go`) and everything downstream —
  merging via go-jsons, struct tags, gjson/sjson reads and writes, the
  generated schema — stays JSON. Selected string fields are shell-expanded at
  load time by the resolver in `internal/config/resolve.go`.
- **Persistence**: SQLite + sqlc. All queries live in `internal/db/sql/`,
  generated code in `internal/db/`. Migrations in `internal/db/migrations/`.
- **Pub/sub**: `internal/pubsub` for decoupled communication between agent,
  UI, and services.
- **Hooks**: User-defined shell commands in the config (`hooks:` block)
  that fire on lifecycle events (Pre/PostToolUse, UserPromptSubmit,
  SessionStart, Stop, SubagentStop, Notification, Pre/PostCompact). The
  engine (`internal/hooks/`) is independent of fantasy — it takes inputs,
  runs commands, returns decisions. The `hooks.Registry` reads the live
  config per event (nil-safe); the `hookedTool` decorator in
  `internal/agent/hooked_tool.go` wraps tools at the coordinator level,
  and the prompt/turn/compact events fire from `sessionAgent.Run`.
  See `docs/hooks/README.md` for the user-facing protocol.
- **CGO disabled**: builds with `CGO_ENABLED=0` and
  `GOEXPERIMENT=greenteagc`.

## Build/Test/Lint Commands

- **Build**: `go build .` or `go run .`
- **Test**: `just test` or `go test ./...` (run single test:
  `go test ./internal/llm/prompt -run TestGetContextFromPaths`)
- **Update Golden Files**: `go test ./... -update` (regenerates `.golden`
  files when test output changes)
  - Update specific package:
    `go test ./internal/tui/components/core -update` (in this case,
    we're updating "core")
- **No API key is needed for any test**. `TestCoderAgent` drives a scripted
  model (`internal/agent/scripted_model_test.go`) rather than a recorded
  provider, so editing a prompt template or a tool description cannot
  invalidate it. To add a case, script the turns the model should take and
  assert on what the agent loop did with them.
- **Lint**: `just lint-fix`
- **Format**: `just fmt` (`golangci-lint fmt`). This applies the same gofumpt
  `just lint` gates on, so formatting and linting can never disagree. Do not
  reach for a standalone `gofumpt` binary - see **Formatting** below.
- **Modernize**: `just modernize` (runs `modernize` which makes code
  simplifications)
- **Dev**: `just dev` (runs with profiling enabled)

## Code Style Guidelines

- **Imports**: Use `goimports` formatting, group stdlib, external, internal
  packages.
- **Formatting**: gofumpt (stricter than gofmt), applied and enforced
  through golangci-lint. Run it with `just fmt`.
- **Naming**: Standard Go conventions — PascalCase for exported, camelCase
  for unexported.
- **Types**: Prefer explicit types, use type aliases for clarity (e.g.,
  `type AgentName string`).
- **Error handling**: Return errors explicitly, use `fmt.Errorf` for
  wrapping.
- **Context**: Always pass `context.Context` as first parameter for
  operations.
- **Interfaces**: Define interfaces in consuming packages, keep them small
  and focused.
- **Structs**: Use struct embedding for composition, group related fields.
- **Constants**: Use typed constants with iota for enums, group in const
  blocks.
- **Testing**: Use testify's `require` package, parallel tests with
  `t.Parallel()`, `t.SetEnv()` to set environment variables. Always use
  `t.Tempdir()` when in need of a temporary directory. This directory does
  not need to be removed.
- **JSON tags**: Use snake_case for JSON field names.
- **File permissions**: Use octal notation (0o755, 0o644) for file
  permissions.
- **Log messages**: Log messages must start with a capital letter (e.g.,
  "Failed to save session" not "failed to save session").
  - This is enforced by `just lint-log` which runs as part of `just lint`.
- **Comments**: End comments in periods unless comments are at the end of the
  line.

## Testing with Mock Providers

When writing tests that involve provider configurations, use the mock
providers to avoid API calls:

```go
func TestYourFunction(t *testing.T) {
    // Enable mock providers for testing
    originalUseMock := config.UseMockProviders
    config.UseMockProviders = true
    defer func() {
        config.UseMockProviders = originalUseMock
        config.ResetProviders()
    }()

    // Reset providers to ensure fresh mock data
    config.ResetProviders()

    // Your test code here - providers will now return mock data
    providers := config.Providers()
    // ... test logic
}
```

## Formatting

- ALWAYS format any Go code you write, with `just fmt`.
  - A standalone `gofumpt` binary is version-coupled to the Go it was built
    with, and neither direction is safe here. Built before this module's Go,
    it fails with "method must have no type parameters" on
    `internal/app/app.go` and formats nothing. Built after, it reformats
    `internal/cmd/session.go` into a shape `just lint` rejects.
  - `just fmt` goes through golangci-lint, which carries the gofumpt the
    lint gate uses, so it is correct in both directions.

## Comments

- Comments that live on their own lines should start with capital letters and
  end with periods. Wrap comments at 78 columns.

## Committing

- ALWAYS use semantic commits (`fix:`, `feat:`, `chore:`, `refactor:`,
  `docs:`, `sec:`, etc).
- Try to keep commits to one line, not including your attribution. Only use
  multi-line commits when additional context is truly necessary.

## Working on the TUI (UI)

Anytime you need to work on the TUI, read `internal/ui/AGENTS.md` before
starting work.

## Styling System

The styling system lives in `internal/ui/styles/` and is organized into
three layers:

- **`quickstyle.go`**: The stable base theme builder. `quickStyle(opts)`
  constructs a `Styles` struct from `quickStyleOpts` — a palette of
  design tokens (primary, secondary, fgBase, bgBase, success, error, etc.).
  `quickStyle` must be fully token-driven: never hardcode specific
  `charmtone.*` colors here (except Chroma syntax highlighting, which is
  pending tokenization). This lets any theme reuse the base without
  inheriting Charmtone-specific colors.
- **`themes.go`**: Defines concrete themes. Each theme function (e.g.
  `CharmtonePantera`) calls `quickStyle` with its palette, then applies
  theme-specific overrides as needed.
- **`styles.go`**: Defines the `Styles` struct and its documentation —
  the shape of what `quickStyle` produces.

**Adding theme-specific overrides**: When a style genuinely needs a
color that doesn't fit the token model (e.g. the bang prompt uses
Salt/Hazy/Larple), keep `quickStyle` on the closest semantic token and
override only the differing colors in the theme function:

```go
func CharmtonePantera() Styles {
	s := quickStyle(quickStyleOpts{ /* palette */ })

	// Override only the colors that differ from the token defaults.
	s.Editor.PromptBangIconFocused = s.Editor.PromptBangIconFocused.
		Foreground(charmtone.Salt).
		Background(charmtone.Hazy)

	return s
}
```

**Adding a new theme**: Add a palette function in `themes.go` that
returns a `quickStyleOpts` (plus an overrides function when the theme
needs colors outside the token model), then register both in the
`builtinThemes` / `builtinThemeOverrides` maps. Users select the theme
via `options.tui.theme`, or the theme picker (alt+t) which writes it; a
configured theme wins over the provider-based `ThemeForProvider`
mapping.
