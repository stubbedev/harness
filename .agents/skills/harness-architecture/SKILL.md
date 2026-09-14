---
name: harness-architecture
description:
  Use when you need the map of the Harness codebase - which package or file owns
  a responsibility, what a dependency (fantasy, bubbletea, lipgloss, sqlc) is
  there for, how the model catalog is assembled, or the recurring patterns
  (csync, pubsub, context files, permissions) the code is built on.
---

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
    catalog.go                     Shared catalog cache (SQLite, machine-wide)
  catalog/                         Provider and model catalog built from models.dev
    modelsdev.go                   Fetch and translate https://models.dev/api.json
    openrouter.go                  OpenRouter's own model API, for its entry
    known.go                       The hand-maintained bits models.dev omits
    seed.json.gz                   Snapshot bundled in the binary (go generate)
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
  extensions/                      Lua extension host: sandboxed VMs, registered tools/hooks/commands
    extensions.go                  Directory discovery, name validation, dedup
    runtime.go                     Per-extension LState: sandbox, serialised calls, timeouts
    api.go / api_host.go           The `harness` table: registration, then the gated capabilities
    host.go                        Host: loads every extension, owns tools/commands/dispatch
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
- **`sqlc`**: Generates Go code from SQL queries in `internal/db/sql/`.

### The Model Catalog

Providers and models come from [models.dev](https://models.dev), fetched
live and cached in a SQLite database shared by every workspace
(`$XDG_DATA_HOME/harness/catalog`), with the OpenRouter entry refreshed
from OpenRouter's own model API. `harness update-providers` refreshes
that same store.

models.dev does not publish everything harness needs. `internal/catalog/known.go`
holds the rest, and it is the only hand-maintained surface: the wire
protocol and base URL for providers whose entry names a vendor SDK
instead of an OpenAI-compatible endpoint, the `$VAR` endpoint templates
that keep native providers overridable, per-provider headers, and the
map from the provider ids catwalk used to the ids models.dev uses (a
config written against the old catalog is migrated at load time).

`internal/catalog/seed.json.gz` is a snapshot of the translated catalog,
embedded in the binary so a first run with no network still has
providers. Refresh it with `go generate ./internal/catalog`.

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
- **Extensions**: Lua programs loaded into the binary (gopher-lua) from
  `extensions_paths`, each a directory with an `init.lua`. They register
  agent tools, hook handlers and palette commands through the `harness`
  table, which is the only thing the sandboxed VM can reach. The host
  (`internal/extensions/`) is built in `app.New`, handed to the
  coordinator, and attached to `hooks.Registry` as a `Dispatcher` so Lua
  handlers aggregate with shell hooks. Privileged host functions (fs,
  exec, http) fire a `PreToolUse` hook of their own through a separate
  registry, so a policy still sees them and a VM is never re-entered.
  See `docs/extensions/README.md` for the user-facing API.
- **CGO disabled**: builds with `CGO_ENABLED=0` and
  `GOEXPERIMENT=greenteagc`.
