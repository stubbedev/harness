# Config

> [!NOTE]
> This document was designed for both humans and agents.

> [!TIP]
>
> Harness can configure itself via a builtin config skill. That is to say, you
> can generally just tell Harness what you want to configure using natural
> language.

Harness is configured with YAML. One format, one schema, one file to learn:

```yaml
# ~/.config/harness/config.yaml
providers:
  anthropic:
    api_key: ${ANTHROPIC_API_KEY}

models:
  large:
    provider: anthropic
    model: claude-sonnet-4-20250514

permissions:
  allowed_tools: [view, ls, grep]

options:
  tui:
    theme: gruvbox-dark
```

Secrets never have to live in the file: values are expanded through the
embedded shell at load time, so `${ANTHROPIC_API_KEY}` and
`$(op read op://private/anthropic/key)` both work. See
[Environment interpolation](#environment-interpolation).

## Where config lives

Harness merges everything it finds, with later entries winning:

| Order | Path                                     | Who writes it       |
| ----- | ---------------------------------------- | ------------------- |
| 1     | `/etc/harness/config.yaml` (Unix only)   | your administrator  |
| 2     | `$XDG_CONFIG_HOME/harness/config.yaml`   | you                 |
| 3     | `$XDG_DATA_HOME/harness/state.yaml`      | Harness             |
| 4     | `<project>/harness.yaml`                 | you                 |
| 5     | `<project>/.harness.yaml`                | you                 |
| 6     | `<data-directory>/state.yaml`            | Harness             |

On Windows the user config is `%XDG_CONFIG_HOME%\harness\config.yaml` (falling
back to `%USERPROFILE%\.config\harness\config.yaml`) and the state file lives
under `%LOCALAPPDATA%\harness`. `.yml` works everywhere `.yaml` does, and
loses to `.yaml` when both are present.

Project files are discovered from the current directory upward, stopping at the
git working tree root, so a stray `harness.yaml` above your project is never
adopted. Deeper directories win over shallower ones.

Two locations are **machine-owned**: `state.yaml` in the global data directory
and in the project's data directory (`.harness/` by default). Harness writes
API keys you paste into the TUI, OAuth tokens, the selected and recently-used
models, and UI preferences there. Those writes round-trip through JSON, so
comments in a `state.yaml` will not survive. The files you author are never
rewritten — comments and layout in them are safe.

`harness dirs` prints the locations in use.

### Editor support

The config is described by a JSON Schema, which every YAML language server
consumes:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/stubbedev/harness/main/schema.json
```

`harness schema` prints the schema from the running binary, which is handy for
a version-pinned local copy:

```bash
harness schema > ~/.config/harness/schema.json
```

## Security

The config is trusted input. Any `$(...)` in it runs at load time with your
privileges, before the UI appears, and hook commands run whenever their event
fires. Guard the file, and don't launch Harness in a directory whose
`harness.yaml` you haven't read.

## Providers

`providers` is a map keyed by provider ID. Known providers need only what you
want to override, which is usually the key:

```yaml
providers:
  anthropic:
    api_key: ${ANTHROPIC_API_KEY}
  openai:
    api_key: ${OPENAI_API_KEY}
    extra_headers:
      OpenAI-Organization: ${OPENAI_ORG_ID}
```

A header whose value resolves to the empty string — an unset variable, a
`$(...)` that prints nothing, a literal `""` — is dropped from the outgoing
request, which makes env-gated headers like the one above safe to leave in
place.

Unknown providers need a type and a base URL:

```yaml
providers:
  deepseek:
    type: openai-compat
    base_url: https://api.deepseek.com/v1
    api_key: ${DEEPSEEK_API_KEY:?set DEEPSEEK_API_KEY}

  ollama:
    type: ollama
    base_url: http://localhost:11434/v1
    discover_models: true
```

| Field                  | Meaning                                                        |
| ---------------------- | -------------------------------------------------------------- |
| `name`                 | Display name                                                   |
| `type`                 | `anthropic`, `openai`, `openai-compat`, `azure`, `bedrock`, `vertexai`, or a local type (`ollama`, `lmstudio`, `llamacpp`) |
| `base_url`             | API base URL                                                   |
| `api_key`              | API key; expanded at load time                                 |
| `disable`              | Keep the entry but stop using it                               |
| `flat_rate`            | Bill as flat rate rather than per token                        |
| `discover_models`      | Ask the endpoint what models it serves and merge them in       |
| `system_prompt_prefix` | Text prepended to the system prompt for this provider          |
| `extra_headers`        | Extra HTTP headers (map)                                       |
| `extra_body`           | Object merged into request bodies (**not** expanded)           |
| `provider_options`     | Provider-specific object merged into requests                  |
| `aws_auth_refresh`     | Command that refreshes AWS credentials                         |
| `models`               | Custom models offered by this provider                         |

### Custom models

```yaml
providers:
  work:
    type: openai-compat
    base_url: https://api.example.com/v1
    api_key: $(op read op://work/harness/key)
    models:
      - id: internal-large
        name: Internal Large
        context_window: 128000
        default_max_tokens: 8192
        can_reason: true
        reasoning_levels: [low, medium, high]
        supports_attachments: false
        cost_per_1m_in: 3.0
        cost_per_1m_out: 15.0
```

## Models

`models` picks which model fills each slot. `large` is the coding model;
`small` handles summarization and other cheap work.

```yaml
models:
  large:
    provider: anthropic
    model: claude-sonnet-4-20250514
    max_tokens: 16384
    think: true
    reasoning_effort: medium
  small:
    provider: anthropic
    model: claude-haiku-4-20250514
```

Besides `provider` and `model`: `max_tokens`, `think`, `reasoning_effort`
(models with reasoning levels only), `temperature`, `top_p`, `top_k`,
`frequency_penalty`, `presence_penalty`, and `provider_options`.

`harness models` lists available IDs in `provider/model` form. Model choices
made in the TUI are written to the state file, so the config only needs this
block when you want to pin a model for a project or a machine.

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
    disabled_tools: [write_file]
```

| Field                                                       | Meaning                                     |
| ----------------------------------------------------------- | ------------------------------------------- |
| `type`                                                      | `stdio`, `http`, or `sse`                   |
| `command`, `args`, `env`                                    | For `stdio` servers                         |
| `url`, `headers`                                            | For `http` and `sse` servers                |
| `timeout`                                                   | Seconds before the server is considered dead |
| `disabled`                                                  | Keep the entry, stop starting it            |
| `enabled_tools`, `disabled_tools`                           | Allow/deny individual tools                 |
| `sessionless`                                               | Treat the server as stateless               |
| `oauth`, `oauth_client_id`, `oauth_client_secret`, `oauth_callback_port` | OAuth flow for remote servers   |

## Language servers

```yaml
lsp:
  go:
    command: gopls
    filetypes: [go, mod]
    env:
      GOPATH: ${HOME}/go
    options:
      gofumpt: true
      staticcheck: true

  typescript:
    command: typescript-language-server
    args: [--stdio]
```

Fields: `command`, `args`, `env`, `filetypes`, `root_markers`, `init_options`
(sent in the LSP `initialize` request), `options` (server settings), `timeout`,
and `disabled`. Harness fills in defaults for servers it recognizes, so a bare
`command` is usually enough.

## Permissions and tools

```yaml
permissions:
  yolo: false
  allowed_tools: [view, ls, grep, edit]

options:
  disabled_tools: [bash]

tools:
  ls:
    max_depth: 5
    max_items: 500
  grep:
    timeout: 45s
  glob:
    timeout: 2m
```

- `permissions.yolo` unset means prompts are skipped; set it to `false` to get
  them back.
- `permissions.allowed_tools` lists tools that skip the prompt.
- `options.disabled_tools` hides tools from the agent entirely — stronger than
  prompting, since the agent never sees them.
- Tool timeouts take a duration string (`45s`, `2m`, `1h30m`).

## Hooks

```yaml
hooks:
  PreToolUse:
    - name: no-haskell
      matcher: ^bash$
      command: .harness/hooks/no-haskell.sh
      timeout: 10
```

`matcher` is a regex against the tool name; omit it to match every tool.
`timeout` is in seconds and defaults to 30. Project hooks take precedence over
global ones, matching hooks are deduplicated by `command`, run in parallel, and
are aggregated in config order. See [Hooks](../hooks/README.md) for the
stdin/stdout contract.

## Options

Everything else about Harness's behavior:

```yaml
options:
  debug: false # verbose logging
  debug_lsp: false # log LSP traffic too
  auto_lsp: true # start language servers automatically
  progress: true # progress output in non-interactive runs
  init_prompt: true # offer to initialize a project with no context file
  initialize_as: AGENTS.md # filename written by that initialization
  data_directory: .harness # per-project state and logs
  notifications: auto # auto | native | osc | bell | disabled
  request_timeout: 60 # seconds of inactivity per model request; 0 disables it
  max_retries: 3 # retries for a failing request

  disable_auto_summarize: false # summarize a session when context fills up
  auto_summarize_ratio: 0.2 # share of a <=200k window kept free (default 0.2)
  auto_summarize_buffer: 20000 # tokens kept free in a >200k window

  disable_metrics: false
  disable_update_check: false
  disable_provider_auto_update: false # stop refreshing the provider catalog
  disable_default_providers: false # only use providers defined here

  context_paths: [.cursorrules] # extra per-project context files
  global_context_paths: [~/.config/harness/AGENTS.md]
  skills_paths: [./skills]
  disabled_skills: [harness-config]
  subagents_paths: [./agents]
  disabled_subagents: [reviewer]
  max_concurrent_subagents: 24 # sub-agents running at once; extras wait for a slot

  attribution:
    trailer_style: assisted-by # none | co-authored-by | assisted-by
    generated_with: true # add the "Generated with" trailer
```

> [!IMPORTANT]
> These skill paths load by default — you do NOT need `skills_paths` for them:
> `.agents/skills`, `.harness/skills`, `.claude/skills`, `.cursor/skills`.

### TUI

```yaml
options:
  tui:
    theme: charmtone # charmtone | catppuccin-mocha | gruvbox-dark
    compact_mode: false # hide the sidebar
    diff_mode: unified # unified | split
    transparent: false # use the terminal background
    mouse: true # false lets the terminal or tmux own selection and copy
    scrollbar: default # default | always | never
    exit_banner: default # default | compact | none
    git_status: true # branch and working-tree status in the header
    show_thinking: true # render reasoning blocks in the transcript
    textarea_min_height: 3 # collapsed height of the prompt; it grows to fit
    completions:
      max_depth: 5
      max_items: 500
```

The theme picker (`alt+t`) and the command palette's "Disable Background Color"
and "Disable Mouse" toggles write to the **global state file**. If a project
config also sets `theme`, `transparent`, or `mouse`, the project wins on the
next launch (see [Where config lives](#where-config-lives)), so a toggle can
look like it silently reverted.

## Environment interpolation

Selected string fields are expanded through the embedded shell — the same
interpreter the `bash` tool uses — at load time:

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
`${VAR:?message}`, and `$(command)`. An unset variable expands to empty, so use
`${VAR:?message}` for anything required and the load fails loudly instead of
authenticating with an empty key. A failing `$(command)` is always a hard
error.

The top-level `env` block is applied before providers are configured, so it can
feed credential chains that read the environment directly:

```yaml
env:
  AWS_PROFILE: work
  ANTHROPIC_API_KEY: $(op read op://private/anthropic/key)
```

### YAML quoting

`${VAR}` and `$(cmd)` need no quotes. Quote when a value starts with `*`, `&`,
`%`, `@`, or a backtick, when it contains `: ` or ` #`, and when you want a
string that YAML would otherwise read as a number, a boolean, or null —
`yes`, `no`, `on`, `off`, and `null` included:

```yaml
providers:
  example:
    extra_headers:
      X-Flag: "no" # without quotes this is the boolean false
```

## Composing configs

YAML has no `include`, and that is deliberate: the layering does the composing.
Put what you want everywhere in `~/.config/harness/config.yaml` and let each
project's `harness.yaml` override it. Within a project, `.harness.yaml` wins
over `harness.yaml`, which is a convenient place for personal settings you
keep out of version control.

Merging is per key and deep: a map in a later file merges into the earlier
one, and a list is appended to the earlier one rather than replacing it. So a
project that sets `context_paths` adds to whatever the user config listed. To
drop something a lower layer defined, override it in place — disable a
provider rather than trying to unset it:

```yaml
providers:
  openai:
    disable: true
```

