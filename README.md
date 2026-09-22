# Harness

<p align="center">
    <a href="https://github.com/stubbedev/harness/releases"><img src="https://img.shields.io/github/release/stubbedev/harness" alt="Latest Release"></a>
    <a href="https://github.com/stubbedev/harness/actions"><img src="https://github.com/stubbedev/harness/actions/workflows/ci.yml/badge.svg" alt="Build Status"></a>
</p>

Harness is a terminal-based AI coding assistant, forked from
[Crush](https://github.com/charmbracelet/crush) and maintained independently.

## Features

- **Multi-Model:** choose from a wide range of LLMs or add your own via OpenAI- or Anthropic-compatible APIs
- **Flexible:** switch LLMs mid-session while preserving context
- **Session-Based:** maintain multiple work sessions and contexts per project
- **LSP-Enhanced:** Harness uses LSPs for additional context, just like you do
- **Extensible:** add capabilities via MCPs (`http`, `stdio`, and `sse`), or in-process with Lua extensions
- **Works Everywhere:** first-class support in every terminal on macOS, Linux, Windows (PowerShell and WSL), Android, FreeBSD, OpenBSD, and NetBSD

## Installation

### Nix (recommended)

The flake in this repository builds Harness, its shell completions, and its man
page:

```bash
# Run it without installing.
nix run github:stubbedev/harness

# Or add it to a profile.
nix profile install github:stubbedev/harness
```

In a flake-based NixOS or Home Manager configuration:

```nix
{
  inputs.harness.url = "github:stubbedev/harness";

  # …then, in your module:
  # environment.systemPackages = [ inputs.harness.packages.${pkgs.system}.default ];
  # home.packages = [ inputs.harness.packages.${pkgs.system}.default ];
}
```

CI builds every commit and pushes the closure to a binary cache, so you can
substitute it instead of compiling:

```nix
nix.settings = {
  substituters = [ "https://nix.stubbe.dev/c/default/default" ];
};
```

Harness inherits Crush's FSL-1.1-MIT license, which nixpkgs classifies as
unfree. The flake allows it for its own nixpkgs instance, so nothing is
required on your side; pulling the package into a configuration that builds it
against your own nixpkgs may need `allowUnfree`.

### Homebrew

On macOS or Linux (Homebrew on Linux x86_64/arm64):

```bash
brew install stubbedev/tap/harness
```

The formula installs the prebuilt release binary for your platform and is
updated automatically on every release.

### Go

```bash
go install github.com/stubbedev/harness@latest
```

On illumos (OpenIndiana, OmniOS), the command above works as-is. Only native
OS notifications are unavailable there; terminal-based notifications (OSC) and
the terminal bell still work. On Oracle Solaris, add `-tags sqlite3_dotlk` so
the local database uses dot-file locking:

```bash
go install -tags sqlite3_dotlk github.com/stubbedev/harness@latest
```

Tagged releases also carry prebuilt binaries for Linux, macOS, Windows, and the
BSDs: see [Releases](https://github.com/stubbedev/harness/releases).

## Getting Started

Press <kbd>ctrl+l</kbd> to open the model picker, choose a provider, and paste
your API key. Harness stores it in its state file and you're off.
## API Keys

You can also use Harness with many other providers such as Anthropic, OpenAI,
Gemini, OpenRouter and so on. Press <kbd>ctrl+l</kbd> to open the model picker,
choose the provider of your choice, and paste your API key.

That said, you can also set environment variables for preferred providers:

| Environment Variable        | Provider                                           |
| --------------------------- | -------------------------------------------------- |
| `ANTHROPIC_API_KEY`         | Anthropic                                          |
| `OPENAI_API_KEY`            | OpenAI                                             |
| `VERCEL_API_KEY`            | Vercel AI Gateway                                  |
| `GEMINI_API_KEY`            | Google Gemini                                      |
| `ZAI_API_KEY`               | Z.ai                                               |
| `MINIMAX_API_KEY`           | MiniMax                                            |
| `SYNTHETIC_API_KEY`         | Synthetic                                          |
| `HF_TOKEN`                  | Hugging Face Inference                             |
| `CEREBRAS_API_KEY`          | Cerebras                                           |
| `OPENROUTER_API_KEY`        | OpenRouter                                         |
| `IONET_API_KEY`             | io.net                                             |
| `ALIBABA_SINGAPORE_API_KEY` | Alibaba (Singapore)                                |
| `ALIBABA_US_API_KEY`        | Alibaba (United States)                            |
| `GROQ_API_KEY`              | Groq                                               |
| `AVIAN_API_KEY`             | Avian                                              |
| `OPENCODE_API_KEY`          | OpenCode Zen & Go                                  |
| `VERTEXAI_PROJECT`          | Google Cloud VertexAI (Gemini)                     |
| `VERTEXAI_LOCATION`         | Google Cloud VertexAI (Gemini)                     |
| `AWS_ACCESS_KEY_ID`         | Amazon Bedrock (Claude)                            |
| `AWS_SECRET_ACCESS_KEY`     | Amazon Bedrock (Claude)                            |
| `AWS_REGION`                | Amazon Bedrock (Claude)                            |
| `AWS_PROFILE`               | Amazon Bedrock (Custom Profile)                    |
| `AWS_BEARER_TOKEN_BEDROCK`  | Amazon Bedrock                                     |
| `AZURE_OPENAI_API_ENDPOINT` | Azure OpenAI models                                |
| `AZURE_OPENAI_API_KEY`      | Azure OpenAI models (optional when using Entra ID) |
| `AZURE_OPENAI_API_VERSION`  | Azure OpenAI models                                |
| `MOONSHOT_API_KEY`          | Moonshot                                           |

Also note that Harness can support nearly any provider, including
[Local Models](#local-models). For more info see
[Custom Providers](#custom-providers) below.

### By the Way

The default model listing comes from [models.dev](https://models.dev), an open
source catalog of models and providers that Harness fetches at startup (see
[Provider Auto-Updates](#provider-auto-updates) to pin or disable that). A
provider missing from the catalog can always be added by hand — see
[Custom Providers](#custom-providers).

## Configuration

> [!TIP]
> Harness ships with a builtin skill for configuring itself. Most of the time
> you can just tell what you want it to configure and it will get the job done.

Harness runs great with no configuration. That said, if you do need or want to
customize Harness, you can, with YAML.

For example:

```yaml
providers:
  # Add Ollama, and register a model on it.
  ollama:
    type: ollama
    base_url: http://localhost:11434/v1
    models:
      - id: llama3.3
        name: Llama 3.3
        context_window: 128000

mcp:
  # Add an MCP server, with a GitHub API token stored in 1Password.
  github:
    type: http
    url: https://api.github.com/mcp/
    headers:
      Authorization: Bearer $(op read 'op://my-secret-key')
```

Values are expanded through Harness's built-in shell at load time, so
`${GITHUB_TOKEN}`, `${VAR:?required}` and `$(...)` all work — and work
identically on every platform, Windows included.

Configuration can be added either local to the project itself, or globally,
with the following priority:

| Priority | Unix-like                       | Windows                                      |
| -------- | ------------------------------- | -------------------------------------------- |
| 1        | `./.harness.yaml`               | `.\.harness.yaml`                            |
| 2        | `./harness.yaml`                | `.\harness.yaml`                             |
| 3        | `~/.config/harness/config.yaml` | `%USERPROFILE%\.config\harness\config.yaml`  |
| 4        | `/etc/harness/config.yaml`      | —                                            |

(Harness respects the [XDG Base Directory Specification][xdg], so your paths
may differ depending on your `XDG_CONFIG_HOME` value. `.yml` works wherever
`.yaml` does. Data directories such as `~/.local/share/harness` and
`%LOCALAPPDATA%\harness` hold a machine-owned `state.yaml` — API keys, tokens,
model selection, UI preferences — which Harness writes and you generally
should not hand-edit.)

[xdg]: https://specifications.freedesktop.org/basedir-spec/basedir-spec-latest.html

Every setting is described by a JSON Schema, which YAML language servers read
directly — point your editor at it for completion and validation:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/stubbedev/harness/main/schema.json
```

See [the config docs](./docs/config/) for the full reference.

> [!TIP]
> You can override the user and data config locations by setting:
>
> - `HARNESS_GLOBAL_CONFIG`
> - `HARNESS_GLOBAL_DATA`

As an additional note, Harness also stores ephemeral data, such as application
state, in one additional location. This is state and should not be edited by
hand, nor should it be considered configuration.

```bash
# Unix
$HOME/.local/share/harness/state.yaml

# Windows
%LOCALAPPDATA%\harness\state.yaml
```

#### A note on security

Your config is trusted code: any `$(...)` in it runs at load time with your
privileges, and hook commands run whenever their event fires. Don't launch
Harness in a directory whose config you haven't reviewed.

### Environment Variables

The top-level `env` field sets environment variables at startup, before
providers are configured. This is useful for variables that affect provider
authentication (e.g. the AWS SDK credential chain) without wrapping the
`harness` command in a shell script or exporting them in your shell profile:

```yaml
env:
  AWS_PROFILE: my-sso-profile
```

Values support the same `$VAR` and `$(command)` expansion as other config
fields, so you can reference existing environment variables or shell out for
a value.

### LSPs

Harness can use LSPs for additional context to help inform its decisions, just
like you would. LSPs can be added manually like so:

```yaml
lsp:
  go:
    command: gopls
    env:
      GOTOOLCHAIN: go1.24.5
  typescript:
    command: typescript-language-server
    args: [--stdio]
  nix:
    command: nil
```

### MCPs

Harness also supports Model Context Protocol (MCP) servers through three transport
types: `stdio` for command-line servers, `http` for HTTP endpoints, and `sse`
for Server-Sent Events.

```yaml
mcp:
  # A local MCP server that runs a Node.js script.
  filesystem:
    type: stdio
    command: node
    args: [/path/to/mcp-server.js]
    timeout: 10
    disabled_tools: [some-tool-name]
    env:
      NODE_ENV: production

  # A GitHub MCP server that uses an API token.
  github:
    type: http
    url: https://api.github.com/mcp/
    timeout: 10
    headers:
      Authorization: Bearer ${GH_PAT}
    disabled_tools: [create_issue, create_pull_request]

  # A streaming MCP server that uses SSE.
  streaming-service:
    type: sse
    url: https://example.com/mcp/sse
    timeout: 10
    headers:
      API-Key: ${API_KEY}
```

#### MCP OAuth

HTTP and SSE MCP servers that require OAuth can use Harness's built-in
authorization-code flow instead of a static `Authorization` header. Set
`"oauth": true` to enable it:

```yaml
mcp:
  linear:
    type: http
    url: https://mcp.linear.app/mcp
    oauth: true
```

##### Pre-registered clients

Some servers (GitHub, Slack) don't support dynamic client registration.
For those, register an OAuth app with the provider and supply the
credentials directly. All values support shell expansion:

```yaml
mcp:
  github:
    type: http
    url: https://api.github.com/mcp/
    oauth: true
    oauth_client_id: Iv1.abc123def456
    oauth_client_secret: ${GITHUB_MCP_SECRET}
    oauth_callback_port: 40704
```

When `oauth_client_id` is set, Harness skips dynamic client registration
and authenticates as the specified client. When omitted, Harness attempts
dynamic registration automatically (works with Linear, Notion, and other
servers that support RFC 7591).

#### Sessionless servers

Some HTTP MCP servers are sessionless — they never issue a
`Mcp-Session-Id` and reject the `subscriptions/listen` stream Harness opens
for list-changed notifications, which would otherwise break the
connection. Harness auto-detects known sessionless servers (GitHub MCP,
`api.githubcopilot.com/mcp`), so those need no extra configuration.

For other sessionless servers, mark them explicitly with
`sessionless: true`; set it to
`false` to force the default behavior for an auto-detected URL. The
tradeoff is that a sessionless server won't push live
tool/prompt/resource list-changed notifications.

### Hooks

Harness has preliminary support for hooks. For details, see
[the hook guide](./docs/hooks/).

### Extensions

Extensions are Lua programs that run inside Harness, registering agent tools,
hook handlers and slash commands. They live in
`~/.config/harness/extensions/<name>/init.lua` (or `.harness/extensions/` in a
project), run in a sandboxed VM with no ambient I/O, and need no interpreter
installed. For details, see [the extension guide](./docs/extensions/).

### Sharing a workspace across clients

When Harness is run against a shared backend (for example two TUIs talking to
the same `harness serve`), clients are grouped into **workspaces** keyed by
their resolved `--cwd`. Two clients with the same `--cwd` join the same
underlying workspace, so they share the session list, message history,
LSP, and MCP state.

Joining is implicit: pointing a second client at the same working directory
attaches it to the existing workspace. Each new invocation, however, starts
in its own fresh session by default. To pick up the conversation another
client already has open, use the session manager (the session picker) and
select it. Sessions surface two signals there:

- `IsBusy` is set while an agent turn is in flight for that session.
- `AttachedClients` reports how many clients are currently viewing it.

A non-zero `AttachedClients` (often combined with `IsBusy`) is the cue that a
session is "in progress" on another client and joining it will mirror that
view live.

The first client to create a workspace fixes its process-wide flags. In
particular, `--debug` follows a **first-wins** rule: later clients that
arrive at the same `--cwd` with a different value do not change the
running workspace. A debug log line is emitted
recording the mismatch, and the workspace keeps the flags it was created
with.

A workspace lives as long as at least one client has an SSE event stream
open against it. When the last stream disconnects, the workspace is torn
down. There is a short grace window right after `POST /v1/workspaces` so a
client that has created the workspace but not yet opened its event stream
does not get reaped before it can attach.

### Global context files

Harness automatically includes two files for cross-project instructions. Think of
these are personal additions to the system prompt.

- `~/.config/harness/HARNESS.md`: Harness-specific rules that would confuse other
  agentic coding tools. If you only use Harness, this is the only one you need to
  edit.
- `~/.config/AGENTS.md`: generic instructions that other coding tools might
  read. Avoid referring to Harness-specific features or workflows here. You
  probably only care about this if you use multiple agentic coding tools and
  want to share instructions between them.

You can customize these paths with `options.global_context_paths`:

```yaml
options:
  global_context_paths:
    # Load a single markdown file.
    - ~/path/to/custom/context/file.md
    # Recursively load all Markdown files in the folder.
    - /full/path/to/folder/of/files/
```

### Ignoring Files

Harness respects `.gitignore` files by default, but you can also create a
`.harnessignore` file to specify additional files and directories that Harness
should ignore. This is useful for excluding files that you want in version
control but don't want Harness to consider when providing context.

The `.harnessignore` file uses the same syntax as `.gitignore` and can be placed
in the root of your project or in subdirectories.

### Disabling Built-In Tools

You can also deny tools, hiding then from the agent entirely:

```yaml
options:
  disabled_tools: [bash, web_search]
```

To disable tools from MCP servers, see the [MCP config section](#mcps).

### Disabling Skills

You can prevent Harness from using certain skills entirely. Disabled skills are
hidden from the agent, including builtin skills and skills discovered from
disk.

```yaml
options:
  disabled_skills: [harness-config]
```

### Agent Skills

Harness supports the [Agent Skills](https://agentskills.io) open standard for
extending agent capabilities with reusable skill packages. Skills are folders
containing a `SKILL.md` file with instructions that Harness can discover and
activate on demand.

The global paths we looks for skills are:

- `$HARNESS_SKILLS_DIR`
- `$XDG_CONFIG_HOME/agents/skills` or `~/.config/agents/skills/`
- `$XDG_CONFIG_HOME/harness/skills` or `~/.config/harness/skills/`
- `~/.agents/skills/`
- `~/.claude/skills/`
- On Windows, we _also_ look at
  - `%LOCALAPPDATA%\agents\skills\` or `%USERPROFILE%\AppData\Local\agents\skills\`
  - `%LOCALAPPDATA%\harness\skills\` or `%USERPROFILE%\AppData\Local\harness\skills\`
- Additional paths configured via `options.skills_paths`

On top of that, we _also_ load skills in your project from the following
relative paths:

- `.agents/skills`
- `.harness/skills`
- `.claude/skills`
- `.cursor/skills`

Or load directories of skills specifically in your config:

```yaml
options:
  skills_paths:
    - ${HOME}/squid-skills
    - ./other-skills
```

You can get started with example skills from [anthropics/skills](https://github.com/anthropics/skills):

```bash
# Unix
mkdir -p ~/.config/harness/skills
cd ~/.config/harness/skills
git clone https://github.com/anthropics/skills.git _temp
mv _temp/skills/* . && rm -rf _temp
```

```powershell
# Windows (PowerShell)
mkdir -Force "$env:LOCALAPPDATA\harness\skills"
cd "$env:LOCALAPPDATA\harness\skills"
git clone https://github.com/anthropics/skills.git _temp
mv _temp/skills/* . ; rm -r -force _temp
```

#### User-Invocable Skills

Every skill is user-invocable by default and searchable from the skills
palette (<kbd>/</kbd>). Set `user-invocable: false` to keep a skill out of
the palette (background knowledge only the model should load):

```yaml
---
name: my-hot-skill
description: A skill that stays out of the / palette.
user-invocable: false
---
```

Skills appear in the palette with a `user:` or `project:` prefix:

- Skills from global directories show as `user:skill-name`
- Skills from project directories show as `project:skill-name`

When invoked, the skill's instructions are loaded into the conversation context.

To prevent the model from auto-triggering a skill (while still allowing user invocation), add `disable-model-invocation: true`:

```yaml
---
name: my-skill
description: Only invocable by users, not the model.
disable-model-invocation: true
---
```

Skills with `disable-model-invocation` won't appear in the model's available skills list but can still be invoked manually by users.

### Desktop notifications

Harness sends desktop notifications when the agent asks a question and when
it finishes its turn. They're only sent when the terminal window isn't
focused _and_ your terminal supports reporting the focus state.

```yaml
options:
  # Choose auto, native, osc, bell, or disabled.
  notifications: disabled
```

`auto` uses native notifications locally and OSC notifications over SSH when
supported.

### Initialization

When you initialize a project, Harness analyzes your codebase and creates
a context file that helps it work more effectively in future sessions. By
default, this file is named `AGENTS.md`, but you can customize the name and
location with the `initialize-as` option:

```yaml
options:
  initialize_as: AGENTS.md
```

This is useful if you prefer a different naming convention or want to place the
file in a specific directory (e.g., `HARNESS.md` or `docs/LLMs.md`). Harness will
fill the file with project-specific context like build commands, code patterns,
and conventions it discovered during initialization.

### Custom Providers

Harness supports custom provider configurations for both OpenAI-compatible and
Anthropic-compatible APIs.

> [!NOTE]
> Note that we support two "types" for OpenAI. Make sure to choose the right one
> to ensure the best experience!
>
> - `openai` should be used when proxying or routing requests through OpenAI.
> - `openai-compat` should be used when using non-OpenAI providers that have OpenAI-compatible APIs.

#### OpenAI-Compatible APIs

Here’s an example configuration for Deepseek, which uses an OpenAI-compatible
API. Don't forget to set `DEEPSEEK_API_KEY` in your environment.

```yaml
providers:
  deepseek:
    type: openai-compat
    base_url: https://api.deepseek.com/v1
    api_key: ${DEEPSEEK_API_KEY}
    models:
      - id: deepseek-chat
        name: Deepseek V3
        context_window: 64000
        default_max_tokens: 5000
        cost_per_1m_in: 0.27
        cost_per_1m_out: 1.1
        cost_per_1m_in_cached: 1.1
        cost_per_1m_out_cached: 0.07
```

#### Anthropic-Compatible APIs

Custom Anthropic-compatible providers follow this format:

```yaml
providers:
  custom-anthropic:
    type: anthropic
    base_url: https://api.anthropic.com/v1
    api_key: ${ANTHROPIC_API_KEY}
    extra_headers:
      anthropic-version: "2023-06-01"
    models:
      - id: claude-sonnet-4-20250514
        name: Claude Sonnet 4
        context_window: 200000
        default_max_tokens: 50000
        can_reason: true
        supports_attachments: true
        cost_per_1m_in: 3
        cost_per_1m_out: 15
        cost_per_1m_in_cached: 3.75
        cost_per_1m_out_cached: 0.3
```

### Amazon Bedrock

Harness currently supports running Anthropic models through Bedrock, with caching disabled.

A Bedrock provider appears once Harness can find AWS credentials. You can
authenticate in one of two ways:

**API key.** Set `AWS_BEARER_TOKEN_BEDROCK` to a Bedrock API key. This is the
simplest option and never expires mid-session.

**AWS credential chain (SSO, profiles, access keys).** Configure AWS the usual
way with `aws configure` or `aws configure sso`. Harness picks up whatever the
AWS SDK credential chain resolves, including `AWS_PROFILE`, `AWS_ACCESS_KEY_ID`
/ `AWS_SECRET_ACCESS_KEY`, or an SSO session. To select a specific profile,
set `AWS_PROFILE` in your shell (`AWS_PROFILE=myprofile harness`) or in the
top-level [`env`](#environment-variables) config.

If you authenticate via AWS SSO, your session expires periodically. Set
`aws_auth_refresh` to a command that refreshes it. When Bedrock returns a
credential error, Harness runs the command, then retries the request in place
(no duplicate messages, no manual restart):

```yaml
env:
  AWS_PROFILE: my-sso-profile

providers:
  bedrock:
    aws_auth_refresh: aws sso login --profile my-sso-profile
  bedrock-europe:
    aws_auth_refresh: aws sso login --profile my-eu-sso-profile
```

- `aws_auth_refresh` — shell command run when AWS credentials expire (e.g. `aws sso login`)

### Vertex AI Platform

Vertex AI will appear in the list of available providers when `VERTEXAI_PROJECT` and `VERTEXAI_LOCATION` are set. You will also need to be authenticated:

```bash
$ gcloud auth application-default login
```

To add specific models to the configuration, configure as such:

```yaml
# Authentication still comes from gcloud and the VERTEXAI_* env vars.
providers:
  vertexai:
    type: google-vertex
    models:
      - id: claude-sonnet-4@20250514
        name: VertexAI Sonnet 4
        context_window: 200000
        default_max_tokens: 50000
        can_reason: true
        supports_attachments: true
        cost_per_1m_in: 3
        cost_per_1m_out: 15
        cost_per_1m_in_cached: 3.75
        cost_per_1m_out_cached: 0.3
```

### Local Models

Harness can auto-discovers models from local providers. Add a custom provider
with `type` set to `llamacpp`, `omlx`, `lmstudio`, `litellm`, or `ollama`
and leave out the models list. Harness will populate the model list
automatically.

```yaml
# Piece of cake.
providers:
  ollama:
    name: Ollama
    type: ollama
    base_url: http://localhost:11434/v1/
```

For llama.cpp (`llama-server`), point at the server's base URL:

```yaml
providers:
  llamacpp:
    name: llama.cpp
    type: llamacpp
    base_url: http://localhost:2222
```

#### Manual Model Configuration

You can still list models explicitly. User-defined models always take
precedence over discovered ones, and any fields you set won't be overwritten
by auto-discovery. Auto discovery will run if the model list is empty for any
`openai-compat` provider or if you pass `"discover_models": true` it will merge
the found models with your hand configured ones.

```yaml
providers:
  ollama:
    name: Ollama
    type: ollama
    base_url: http://localhost:11434/v1/
    discover_models: true
    models:
      - id: qwen3:30b
        name: Qwen 3 30B
        context_window: 256000
        default_max_tokens: 20000
```

The `--discover-models true` flag merges discovered models with the one above;
your explicit model fields win on conflicts.

## Logging

Sometimes you need to look at logs. Luckily, Harness logs all sorts of
stuff. Logs are stored in the workspace data directory under the global data root (`~/.local/share/harness/workspaces/<hash>-<project>/logs/harness.log` on Linux), keyed per project.

The CLI also contains some helper commands to make perusing recent logs easier:

```bash
# Print the last 1000 lines
harness logs

# Print the last 500 lines
harness logs --tail 500

# Follow logs in real time
harness logs --follow
```

Want more logging? Run `harness` with the `--debug` flag, or enable it in your
config:

```yaml
options:
  debug: true
  debug_lsp: true
```

## Crash Reports

When Harness recovers from or dies on a panic, it persists a crash report
with the panic value and its full stack trace to
`~/.local/share/harness/crashes/` on Linux. The reports are plain text and
also record the version, commit, and working directory, so a crash that
closes the terminal still leaves something to debug. The newest 50 are
kept:

```bash
# List crash reports, newest first
harness crashes

# Print one report in full
harness crashes 20260922-143005.123-tui.log
```

The `HARNESS_CRASH_DIR` environment variable moves the reports elsewhere.

## Multiplexer Status

When Harness runs inside a [herdr](https://github.com/herdr) or tmux
pane it reports agent state over the multiplexer's IPC (no hooks, no
per-event subprocesses): `idle`, `working`, or `error`, plus the
session ID. In tmux the state lands in pane user options
(`@harness-state`, `@harness-session`) written through a single
tmux control-mode client, so a status line can surface it:

```tmux
# in ~/.tmux.conf
set -g status-right '#{?#{@harness-state},harness: #{@harness-state},}'

# or per-pane borders
set -g pane-border-status top
set -g pane-border-format ' #{@harness-state} #{pane_title} '
```

The options are removed when Harness exits cleanly.

## Provider Auto-Updates

By default, Harness automatically checks for the latest and greatest list of
providers and models from [models.dev](https://models.dev), the open source
model database, with the OpenRouter entry taken live from OpenRouter's own
model API. The catalog is refreshed at most once a day and cached in the
local SQLite database, so startups stay fast and offline-friendly. When new
providers and models are available, or when model metadata changes, Harness
automatically updates your local configuration.

There is no hand-maintained provider list in Harness. Every models.dev entry
that carries enough information to be usable becomes a provider
automatically: the API format derives from the AI SDK package the entry
targets, the endpoint from its published base URL, and the API key variable
from its declared environment. New providers and models appear upstream with
no Harness release; a provider shows up in the model picker and becomes
usable as soon as the key it declares is set.

Anything models.dev does not know about can still be used by configuring it
by hand — see [Custom Providers](#custom-providers).

### Custom provider catalog

You can override the models.dev base URL for testing or mirroring by setting
the `MODELS_DEV_URL` environment variable (e.g. `export MODELS_DEV_URL=http://localhost:8000`).

### Disabling automatic provider updates

For those with restricted internet access, or those who prefer to work in
air-gapped environments, this might not be want you want, and this feature can
be disabled. When disabled, Harness serves the last catalog cached in the
local database regardless of age and never reaches the network for it.

To disable automatic provider updates in your config:

```yaml
options:
  disable_provider_auto_update: true
```

Or set the `HARNESS_DISABLE_PROVIDER_AUTO_UPDATE` environment variable:

```bash
export HARNESS_DISABLE_PROVIDER_AUTO_UPDATE=1
```

### Manually updating providers

Manually updating providers is possible with the `harness update-providers`
command:

```bash
# Refresh the catalog from models.dev.
harness update-providers

# Update the catalog from a custom models.dev-compatible JSON document.
harness update-providers https://example.com/

# Update providers from a local file.
harness update-providers /path/to/local-providers.json

# For more info:
harness update-providers --help
```

## Metrics

None. Harness sends no usage data anywhere: the analytics client upstream
shipped with was removed, not merely defaulted off. The event hooks in
[`internal/event`](https://github.com/stubbedev/harness/tree/main/internal/event)
are inert no-ops kept only so the call sites still describe what the app
considers notable.

The only outbound requests Harness makes are the ones you can see: the model
provider you configured, the model catalog (see
[Provider Auto-Updates](#provider-auto-updates), which you can disable), and
whatever your MCP servers and tools do.

## Q&A

### Why is clipboard copy and paste not working?

Installing an extra tool might be needed on Unix-like environments.

| Environment         | Tool                     |
| ------------------- | ------------------------ |
| Windows             | Native support           |
| macOS               | Native support           |
| Linux/BSD + Wayland | `wl-copy` and `wl-paste` |
| Linux/BSD + X11     | `xclip` or `xsel`        |

## Contributing

Issues and pull requests are welcome at
[stubbedev/harness](https://github.com/stubbedev/harness).

## License

[FSL-1.1-MIT](https://github.com/stubbedev/harness/raw/main/LICENSE.md)

Harness is a fork of [Crush](https://github.com/charmbracelet/crush) by
Charmbracelet, Inc., and carries its license.
