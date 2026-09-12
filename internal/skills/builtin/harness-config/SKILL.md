---
name: harness-config
description: Use when the user needs help configuring Harness — writing config.yaml or a project harness.yaml, setting up providers, models, LSPs, MCP servers, hooks, skills, permissions, or changing Harness behavior.
---

# Harness Configuration

Harness is configured with YAML. There is one format and one schema; the file
you edit by hand is:

- `$XDG_CONFIG_HOME/harness/config.yaml` (`~/.config/harness/config.yaml`, or
  `%XDG_CONFIG_HOME%\harness\config.yaml` on Windows) — your settings for every
  project.
- `harness.yaml` (or `.harness.yaml`) in a project — settings for that project.

Everything found is deep-merged, with later files winning:

1. `/etc/harness/config.yaml` (Unix only, lowest priority)
2. `$XDG_CONFIG_HOME/harness/config.yaml`
3. `$XDG_DATA_HOME/harness/state.yaml` — machine-owned state, see below
4. project files, from the git root down to the current directory; within one
   directory `harness.yaml` loses to `.harness.yaml`, and `.yaml` beats `.yml`
5. `.harness/state.yaml` — machine-owned workspace state

Harness **writes** to the two `state.yaml` files only: API keys you paste into
the TUI, OAuth tokens, the selected and recently-used models, and UI
preferences such as the theme. Those writes round-trip through JSON, so
comments in a `state.yaml` do not survive. The files you author — `config.yaml`
and any project `harness.yaml` — are never rewritten, so comments and layout
there are safe.

Point your editor at the schema for completion and validation:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/stubbedev/harness/main/schema.json
```

`harness schema` prints the same schema from the running binary.

## Shape

Every top-level key is optional:

```yaml
models: # which model fills each slot
providers: # where models come from and how to authenticate
mcp: # Model Context Protocol servers
lsp: # language servers
hooks: # commands that fire on agent events
options: # everything else about Harness's behavior
permissions: # which tools skip the permission prompt
tools: # per-tool limits (ls, grep, glob)
env: # environment variables set at startup
```

## Providers and models

`providers` is a map keyed by provider ID. Known providers (anthropic, openai,
…) only need what you want to override — usually just the key.

```yaml
providers:
  anthropic:
    api_key: ${ANTHROPIC_API_KEY}

  deepseek:
    type: openai-compat
    base_url: https://api.deepseek.com/v1
    api_key: ${DEEPSEEK_API_KEY:?set DEEPSEEK_API_KEY}

  ollama:
    type: ollama
    base_url: http://localhost:11434/v1
    discover_models: true

  work:
    type: openai-compat
    base_url: https://api.example.com/v1
    api_key: $(op read op://work/harness/key)
    extra_headers:
      X-Org: platform
    models:
      - id: internal-large
        name: Internal Large
        context_window: 128000
        default_max_tokens: 8192
        can_reason: true
        supports_attachments: false
```

Provider fields: `name`, `type` (`anthropic`, `openai`, `openai-compat`,
`azure`, `bedrock`, `vertexai`, or a local type like `ollama`, `lmstudio`,
`llamacpp`), `base_url`, `api_key`, `disable`, `flat_rate`, `discover_models`,
`system_prompt_prefix`, `extra_headers`, `extra_body`, `provider_options`,
`aws_auth_refresh`, `models`.

`models` picks what runs. `large` is the coding model; `small` handles
summarization and other cheap work:

```yaml
models:
  large:
    provider: anthropic
    model: claude-sonnet-4-20250514
    max_tokens: 16384
    think: true
    reasoning_effort: medium # models with reasoning levels only
  small:
    provider: anthropic
    model: claude-haiku-4-20250514
```

Slot fields: `provider`, `model`, `max_tokens`, `think`, `reasoning_effort`,
`temperature`, `top_p`, `top_k`, `frequency_penalty`, `presence_penalty`,
`provider_options`. `harness models` lists the IDs as `provider/model`.

## MCP servers

```yaml
mcp:
  github:
    type: http
    url: https://api.githubcopilot.com/mcp/
    headers:
      Authorization: Bearer ${GH_PAT}
  filesystem:
    type: stdio
    command: node
    args: [/path/to/mcp-server.js]
    env:
      ROOT: ${HOME}/src
    timeout: 30
```

Fields: `type` (`stdio`, `http`, `sse`), `command`, `args`, `env`, `url`,
`headers`, `timeout`, `disabled`, `disabled_tools`, `enabled_tools`,
`sessionless`, `oauth`, `oauth_client_id`, `oauth_client_secret`,
`oauth_callback_port`.

## Language servers

```yaml
lsp:
  go:
    command: gopls
    filetypes: [go, mod]
    env:
      GOPATH: ${HOME}/go
    options:
      staticcheck: true
  typescript:
    command: typescript-language-server
    args: [--stdio]
```

Fields: `command`, `args`, `env`, `filetypes`, `root_markers`, `init_options`,
`options`, `timeout`, `disabled`.

## Permissions and tools

```yaml
permissions:
  yolo: false # false restores prompts; unset means prompts are skipped
  allowed_tools: [view, ls, grep, edit]

options:
  disabled_tools: [bash] # hidden from the agent entirely, not just prompted

tools:
  ls:
    max_depth: 5
    max_items: 500
  grep:
    timeout: 30s
  glob:
    timeout: 30s
```

`allowed_tools` skips the prompt for those tools. `options.disabled_tools`
removes a tool from the agent's toolbox.

## Hooks

```yaml
hooks:
  PreToolUse:
    - name: no-haskell
      matcher: ^bash$ # regex against the tool name; omit to match every tool
      command: .harness/hooks/no-haskell.sh
      timeout: 10 # seconds, default 30
```

See [Hooks runtime](#hooks-runtime) below for the input/output contract.

## Options

```yaml
options:
  # Behavior
  debug: false
  debug_lsp: false
  auto_lsp: true
  progress: true
  initialize_as: AGENTS.md
  data_directory: .harness
  notifications: auto # auto | native | osc | bell | disabled
  request_timeout: 300
  max_retries: 8
  disable_auto_summarize: false
  auto_summarize_ratio: 0.8
  disable_metrics: false
  disable_provider_auto_update: false
  disable_default_providers: false
  disable_update_check: false

  # Paths (lists)
  context_paths: [.cursorrules]
  global_context_paths: [~/.config/harness/AGENTS.md]
  skills_paths: [./skills]
  disabled_skills: [harness-config]
  subagents_paths: [./agents]
  disabled_subagents: [reviewer]

  # Commit/PR attribution
  attribution:
    trailer_style: assisted-by # none | co-authored-by | assisted-by
    generated_with: true

  # TUI
  tui:
    theme: gruvbox-dark # charmtone | catppuccin-mocha | gruvbox-dark
    compact_mode: false
    diff_mode: unified # unified | split
    transparent: false
    mouse: true # false lets the terminal/tmux own selection and copy
    scrollbar: default # default | always | never
    exit_banner: default # default | compact | none
    git_status: true
    show_thinking: true
    textarea_min_height: 1
    completions:
      max_depth: 5
      max_items: 500
```

These skill paths load by default and do **not** need `skills_paths`:
`.agents/skills`, `.harness/skills`, `.claude/skills`, `.cursor/skills`.

## Environment interpolation

Selected string fields are expanded through the embedded shell at load time,
so secrets never have to be written into the file:

| Surface                                                         | Expansion                          |
| --------------------------------------------------------------- | ---------------------------------- |
| Provider `api_key`, `base_url`, `api_endpoint`, `extra_headers` | yes                                |
| Provider `extra_body`                                           | **no** (passed through verbatim)   |
| MCP `command`, `args`, `env`, `headers`, `url`                  | yes                                |
| MCP `oauth_client_id`, `oauth_client_secret`                    | yes                                |
| LSP `command`, `args`, `env`                                    | yes                                |
| Top-level `env`                                                 | yes                                |
| Hook `command`                                                  | runs via `sh -c`, not the resolver |

Supported constructs: `$VAR`, `${VAR}`, `${VAR:-default}`, `${VAR:+alt}`,
`${VAR:?message}`, `$(command)`. An unset variable expands to empty, so use
`${VAR:?message}` for anything required — it fails the load loudly. A failing
`$(command)` is always a hard error. A header that resolves to empty is dropped
from the request.

Two YAML details worth knowing:

- `${VAR}` and `$(cmd)` need no quotes, but `$VAR:` style values and anything
  starting with `*`, `&`, or `%` do. When in doubt, quote.
- Set `env` at the top level to export variables before providers resolve:

```yaml
env:
  AWS_PROFILE: work
  ANTHROPIC_API_KEY: $(op read op://private/anthropic/key)
```

## Hooks runtime

Hooks are user-defined shell commands that fire on agent events. Currently only
`PreToolUse` is supported, which runs before a tool executes.

### How hooks work

1. When a tool is about to be called, all `PreToolUse` hooks with a matching
   `matcher` (or no matcher) run in parallel.
2. Duplicate commands are deduplicated — each unique command runs at most once.
3. The hook receives JSON on **stdin** and hook-specific **environment
   variables**.

Event names are case-insensitive and accept snake_case: `PreToolUse`,
`pretooluse`, `pre_tool_use`, `PRE_TOOL_USE` all work.

### Hook input (stdin)

```json
{
  "event": "PreToolUse",
  "session_id": "abc-123",
  "cwd": "/path/to/project",
  "tool_name": "bash",
  "tool_input": { "command": "ls -la" }
}
```

### Hook environment variables

| Variable                       | Description                                       |
| ------------------------------ | ------------------------------------------------- |
| `HARNESS_EVENT`                | Event name (e.g. `PreToolUse`)                    |
| `HARNESS_TOOL_NAME`            | Name of the tool being called                     |
| `HARNESS_SESSION_ID`           | Current session ID                                |
| `HARNESS_CWD`                  | Current working directory                         |
| `HARNESS_PROJECT_DIR`          | Project root directory                            |
| `HARNESS_TOOL_INPUT_COMMAND`   | Value of `command` from tool input (if present)   |
| `HARNESS_TOOL_INPUT_FILE_PATH` | Value of `file_path` from tool input (if present) |

### Hook output

**Exit code 0** — hook succeeded. Stdout is parsed as JSON:

```json
{ "decision": "allow", "context": "optional context appended to tool result" }
```

- `decision`: `allow` to explicitly allow, `deny` to block, `none` (or omit).
- `reason`: explanation (used when denying).
- `context`: extra context appended to the tool result.
- `updated_input`: replacement JSON for the tool input; last non-empty wins.

**Exit code 2** — the tool call is blocked; stderr is the deny reason.

**Any other exit code** — non-blocking error; the tool call proceeds.

### Decision aggregation

- **Deny wins over allow** — any deny blocks the call.
- **Allow wins over none** — a lone allow lets it proceed.
- Deny reasons and context strings are concatenated (newline-separated).
- For `updated_input`, the last non-empty value wins.

### Claude Code compatibility

Harness also accepts the Claude Code hook output format, so existing hooks work
unchanged:

```json
{
  "hookSpecificOutput": {
    "permissionDecision": "allow",
    "permissionDecisionReason": "Auto-approved",
    "updatedInput": { "command": "echo rewritten" }
  }
}
```

## User-invocable skills

Skills can be invoked as commands. Add `user-invocable: true` to the skill's
YAML frontmatter:

```yaml
---
name: my-skill
description: A skill that can be invoked as a command.
user-invocable: true
---
```

- Global skills appear as `user:skill-name`; project skills as
  `project:skill-name`.
- Add `disable-model-invocation: true` to keep a skill user-only (hidden from
  the model's available-skills list but still manually invocable).

## Environment variables

- `HARNESS_GLOBAL_CONFIG` — directory holding `config.yaml`, overriding the
  XDG location.
- `HARNESS_GLOBAL_DATA` — directory holding `state.yaml`, overriding the XDG
  location.
- `HARNESS_SKILLS_DIR` — override the default skills directory.
- `HARNESS_SUBAGENTS_DIR` — override the default subagents directories.
- `HARNESS_CACHE_DIR` — override the cache directory.

## Security note

The config is trusted input. Any `$(...)` in it runs at load time with the
invoking user's privileges, before the UI appears, and hook commands run
whenever their event fires. Don't launch Harness in a directory whose
`harness.yaml` you haven't read.
