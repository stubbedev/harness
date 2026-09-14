# Harness Development Guide

## Project Overview

Harness is a terminal-based AI coding assistant written in Go, forked from
[Crush](https://github.com/charmbracelet/crush) and maintained independently.
It connects to LLMs and gives them tools to read, write, and execute code. It
supports multiple providers (Anthropic, OpenAI, Gemini, Bedrock, Copilot,
MiniMax, Vercel, and more), integrates with LSPs for code intelligence, and
supports extensibility via MCP servers, agent skills, shell hooks, and Lua
extensions.

The module path is `github.com/stubbedev/harness`.

## Architecture

The package-by-package map of the codebase, the roles of the main
dependencies, how the model catalog is assembled and the patterns the code
is built on live in the `harness-architecture` skill. Load it (via
`skill_search`, or read `.agents/skills/harness-architecture/SKILL.md`)
before working out where something belongs; it is reference material, so it
is kept out of every prompt rather than carried in all of them.

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

