# Hooks

> [!NOTE]
> This document was designed for both humans and agents.

Hooks are user-defined shell scripts that run when various events happen during
the agent lifecycle, allowing you to both build on top of Harness, customize
its behavior, and exert deterministic control over an agent's wily behavior.

Hooks are just shell commands, and were designed to be both simple and future
forward.

### Hot Hook Facts

- Hooks are just shell commands
- Hooks can be written in any language because they’re just executables: Bash, Python, Node, Rust, Haskell, whatever
- Hooks are Claude Code-compatible
- Harness ships with a builtin `harness-hook` skill write, edit, and configure
  hooks; just tell Harness how to configure Harness
- Hooks fire on nine events across the tool, prompt, turn, session, and
  compaction lifecycle (see [Events](#events))
- Hooks run in parallel for speed, but their results compose in config order
  for determinism

### Some things you can do with hooks:

- Block "dangerous" commands: no more `git push -f` or `cabal init`
- Rewrite tool input: turn `node` calls info `deno`, scrub secrets from
  commands, rewrite all mentions of "Haskell" into "Haskell, The Best
  Language", and so on
- React after the fact: annotate tool output, flag lint errors on files the
  agent just touched, halt a runaway turn
- Rewrite or veto prompts before they reach the model: expand shorthand,
  strip secrets, refuse prompts that mention `production.env`
- Inject context: add notes to the model's context whenever certain tools are
  called or whenever a session starts. For example: "remember to run gofumpt
  after editing Go files"
- Observe the lifecycle: log every sub-agent result, notify another system
  when a turn finishes or a session gets compacted
- Log certain tool calls

…And lots more. Show us what you're building!

## Baby's First Hook

Let's just dive into it and make a simple hook. This particular hook will
disallow the use of Haskell (but we love you, Simon Peyton Jones).

### Config

The first thing we need to do is hook up our hook. Let's add the following to
our **project-level** `harness.yaml`. Relative paths like `./no-haskell.sh` work
here because the project root is your working directory. If you are configuring
a global hook (`~/.config/harness/config.yaml`), use an absolute path instead.

```yaml
# As expected, hooks go under a "hooks" key.
hooks:
  # PreToolUse is an event that fires before a tool is used.
  PreToolUse:
    # What tool do we want to hook into? In this case bash, because it runs
    # the stuff we wanna block.
    - matcher: "^bash$"
      # The path to our actual hook script.
      command: ./no-haskell.sh
```

Now, let's make our `no-haskell.sh` hook script.

```bash
#!/usr/bin/env bash

# Disallow ghc, cabal, and stack. Pipe the bash command output
# ($HARNESS_TOOL_INPUT_COMMAND) to grep and match on a regexp.
if echo "$HARNESS_TOOL_INPUT_COMMAND" | grep -qE '(^| )((ghc|cabal|stack)(\.exe)?)( |$)'; then

  # Someone is trying to use Haskell. Let's send a message back to the model
  # and user explaining why we're blocking this. Note that we send all feedback
  # like this to stderr.
  echo "No Haskell allowed, kiddo." >&2

  # Now, block the tool call by exiting with code 2.
  exit 2
fi
```

That's basically it. For the full guide on how hooks work, however, read on.

---

## Execution model

Hooks run through Harness's embedded POSIX shell (`mvdan.cc/sh`) — the same
interpreter the `bash` tool uses. Inline commands and shebang-less scripts
execute in-process; scripts with a `#!` shebang dispatch to the named
interpreter via `os/exec`. This contract is identical on macOS, Linux, and
Windows.

What this means in practice:

- **Windows without Unix tooling**: inline shell (`echo`, pipelines, `jq`,
  `grep`), shebang-less `.sh` scripts, inline PowerShell
  (`powershell -Command …`), and `.exe` invocations all work out of the box
  with no WSL, Git Bash, Cygwin, or MSYS required.
- **PowerShell scripts** (`.ps1`) are not auto-dispatched by extension.
  Invoke them explicitly: `powershell -File ./audit.ps1` (or
  `pwsh -File ./audit.ps1`).
- **Shebang'd scripts** require the named interpreter on `PATH`. Git for
  Windows ships `bash.exe`, which makes `#!/bin/bash` and
  `#!/usr/bin/env bash` scripts work on Windows the same way they do on
  Unix. CRLF line endings in the shebang line are tolerated.
- **Permissive shebang fallback**: if the absolute path in a shebang
  doesn't exist (e.g. `#!/bin/bash` on Windows), Harness falls back to a
  `PATH` lookup of the base name (`bash`) before giving up. A debug-level
  log records the fallback. If the interpreter isn't on `PATH` either, the
  hook fails cleanly as a non-blocking warning and the agent proceeds as
  "no opinion".
- **Environment**: every hook sees `HARNESS=1`, `AGENT=harness`, and
  `AI_AGENT=harness` on top of the `HARNESS_*` hook-specific variables. These
  three markers are guaranteed and match what the `bash` tool sets, so
  scripts that detect "am I being run by an AI agent?" behave the same in
  both contexts.
- **Timeout behavior**: when a hook exceeds its timeout, Harness cancels the
  context and waits a short grace period (~1s) for the interpreter to
  yield. If the hook still hasn't returned, Harness abandons it, logs a
  warning, and treats the result as "no opinion" so the agent can proceed.
  Long-running work should honor context cancellation or run in a
  subprocess via a shebang.

## Configuration

Hooks can be added to your `harness.yaml` (or `.harness.yaml`) at both the
global and project level, with project-level hooks taking precedence.

```yaml
hooks:
  PreToolUse:
    - name: no-rm-rf # friendly name shown in the TUI
      matcher: bash # regex tested against the tool name
      command: ./hooks/my-hot-hook.sh # the path to the hook
      timeout: 10 # in seconds; default 30
```

> [!IMPORTANT]
> The `command` is resolved relative to your **current working directory** —
> not relative to the config file. Relative paths like `./hooks/whatever.sh`
> work fine in a project-level `harness.yaml` because the project root is also
> your working directory. For **global** config (`~/.config/harness/`),
> however, you must use either an absolute path or an inline command:
>
> ```yaml
> # Global ~/.config/harness/config.yaml
> hooks:
>   PreToolUse:
>     - command: /home/you/.config/harness/hooks/no-haskell.sh
>     # or use an inline command:
>     # - command: echo '{"decision":"allow"}'
> ```

Remember, hooks will run in parallel but resolve in config order. Last hook
wins when rewriting input, but first deny wins when blocking.

## Events

These are the events you can hook into:

| Event              | Fires                                                     | Matcher subject | Effect of decisions |
| ------------------ | --------------------------------------------------------- | --------------- | ------------------- |
| `PreToolUse`       | Before every tool call                                    | Tool name       | `deny` blocks the call, `halt` ends the turn |
| `PostToolUse`      | After every tool call completes                           | Tool name       | `deny` appends feedback, `halt` ends the turn |
| `UserPromptSubmit` | After you submit a prompt, before it reaches the model    | —               | `deny`/`halt` blocks the turn |
| `SessionStart`     | On the first prompt of a session                          | —               | Informational; `context` reaches the model |
| `Stop`             | When the top-level agent finishes a turn                  | —               | Informational       |
| `SubagentStop`     | When a dispatched sub-agent finishes                       | Sub-agent type  | Informational; `context` reaches the orchestrator |
| `Notification`     | When a user notification is sent (finished, error, retry) | —               | Informational       |
| `PreCompact`       | Before a session is summarized                            | —               | Informational       |
| `PostCompact`      | After a session was summarized                            | —               | Informational       |

> [!NOTE]
> Event names are case insensitive and snake-caseable, so `PreToolUse`,
> `pretooluse`, `PRETOOLUSE`, `pre_tool_use`, and `PRE_TOOL_USE` all work.
> An unknown event name is a config error — you'll see it at load time,
> not on the first fire.

### PreToolUse

This hook fires before every tool call. Use it to block dangerous commands,
enforce policies, rewrite tool input, inject context the model should see, log
stuff, and so on.

**Matched against**: the tool name (e.g. `bash`, `edit`, `write`,
`mcp_github_create_pull_request`).

**Scope**: `PreToolUse` fires on every tool call, including calls made
from inside sub-agents (`agent` task/fast dispatches, `research`, custom
subagents): they run the same toolset, so a policy has to see their calls to
mean anything. A hook fired from a sub-agent sees the child session's ID in
the payload, which is what distinguishes the call.

### PostToolUse

Fires after a tool call completes, with the tool's response in the payload
(`tool_response.content`, `tool_response.is_error`). The tool already ran, so
nothing can un-run it:

- `deny` does **not** block anything: the reason is appended to the response as
  feedback ("Hook feedback: …") so the model sees it and can react — e.g. run
  the linter after a hook flagged a file.
- `halt` still ends the turn after the tool result is recorded.
- `context` is appended to the tool response.
- `updated_input` is ignored; the input already happened.

**Matched against**: the tool name. Same scope as `PreToolUse`: every tool
call, sub-agent calls included.

### UserPromptSubmit

Fires after you hit Enter but before the prompt reaches the model. This is the
place to:

- Inject context the model should have ("current branch: feat/login; last
  commit: …") — returned `context` is appended to the outbound prompt inside a
  `<hook-context>` block.
- Rewrite the prompt: `updated_prompt` replaces the prompt sent to the model.
  The stored message keeps what you typed, so the transcript stays honest.
- Block the prompt: `deny` (or `halt`) aborts the turn before anything is
  persisted; the reason becomes the run error you see.

It fires on every dispatched prompt, including queued ones that run as their
own turn. Prompts folded into an already-running turn do not re-fire it.

### SessionStart

Fires on the first prompt of a session (a new session, or the first turn after
`/clear`). Informational: decisions are logged, not enforced, but returned
`context` is injected into that first outbound prompt the same way as
`UserPromptSubmit` context.

### Stop

Fires when the top-level agent finishes a turn successfully. Informational —
decisions and `context` are logged only. Use it for logging, metrics, or
notifying external systems that the agent went idle.

### SubagentStop

Fires when a dispatched sub-agent finishes (completed, cancelled, or failed).
The payload carries the sub-agent type and final status. Informational, but
returned `context` is appended to the sub-agent's report so the orchestrating
model sees it — for a background dispatch (`agent` with `background: true`)
the report is the result the orchestrator collects with the `wait` tool.

**Matched against**: the sub-agent type (`task`, `fast`, or a custom agent
name).

### Notification

Fires whenever Harness sends a user notification: agent finished its turn,
agent errored after retries, or a provider request is being retried. The
payload carries `notification_type` and `message`. Informational.

### PreCompact / PostCompact

Fire immediately before and after a session is summarized — either
automatically (context window threshold, `trigger: "auto"`) or on demand
(`/summarize`, `trigger: "manual"`). Informational: a hook cannot veto or
steer a compaction.

Hooks are keyed by event name. Only `command` is required. `matcher` only
applies to events with a subject (the tool events and `SubagentStop`); omit it
to match all.

## Building Hooks

When a hook fires, Harness:

1. Filters hooks whose `matcher` regex matches the tool name (no matcher = match
   all).
2. Deduplicates by `command` (identical commands run once).
3. Runs all matching hooks **in parallel** through Harness's embedded POSIX
   shell (see [Execution model](#execution-model)).
4. Waits for all to finish (or time out), then aggregates results **in config
   order**: deny wins over allow, allow wins over none; `updated_input` patches
   shallow-merge in order.
5. Applies the result before running the tool. If the aggregated decision is
   `deny`, the tool call is blocked. Anything else lets it run.

Note that you can omit `matcher` and match in your shell script instead,
however you'll incur some additional overhead as Harness will still parse and
run each hook.

### Input

Each hook receives data two ways: environment variables and stdin (as JSON).
Environment variables are typically easier to work with, with JSON being
available when input is more complex.

#### Environment Variables

The available environment variables are:

| Variable                       | Description                                          |
| ------------------------------ | ---------------------------------------------------- |
| `HARNESS`                      | Always `1` when running under Harness.               |
| `AGENT`                        | Always `harness`.                                    |
| `AI_AGENT`                     | Always `harness`.                                    |
| `HARNESS_EVENT`                | The hook event name (e.g. `PreToolUse`).             |
| `HARNESS_TOOL_NAME`            | The tool being called, for tool events (e.g. `bash`). |
| `HARNESS_SESSION_ID`           | Current session ID.                                  |
| `HARNESS_CWD`                  | Working directory.                                   |
| `HARNESS_PROJECT_DIR`          | Project root directory.                              |
| `HARNESS_TOOL_INPUT_COMMAND`   | For `bash` calls: the shell command being run.       |
| `HARNESS_TOOL_INPUT_FILE_PATH` | For file tools: the target file path.                |
| `HARNESS_PROMPT`               | For `UserPromptSubmit`/`SessionStart`: the prompt.   |
| `HARNESS_SUBAGENT_TYPE`        | For `SubagentStop`: the sub-agent type.              |
| `HARNESS_TRIGGER`              | For `Pre`/`PostCompact`: `auto` or `manual`.         |
| `HARNESS_NOTIFICATION_TYPE`    | For `Notification`: the notification type.           |
| `HARNESS_MESSAGE`              | For `Notification`/`SubagentStop`: the message/status. |

The `HARNESS`, `AGENT`, and `AI_AGENT` markers are also set by the `bash`
tool, so a script can detect "am I running under Harness?" the same way in
either context.

#### JSON

Standard input provides the full context as JSON:

```jsonc
{
  "event": "PreToolUse", // Hook event name
  "session_id": "313909e", // Current session ID
  "cwd": "/home/user/project", // Working directory
  "tool_name": "bash", // The tool being called
  "tool_input": { "command": "rm -rf /" }, // The tool's input
}
```

Note that `tool_input` field contains the raw JSON the model sent to the tool.

To parse the stdin JSON in your hook script, read from stdin and use a tool like
`jq`:

```bash
#!/usr/bin/env bash
read -r input
tool_name=$(echo "$input" | jq -r '.tool_name')
command=$(echo "$input" | jq -r '.tool_input.command // empty')
```

You can also use tools like Python:

```python
#!/usr/bin/env python3
import json, sys

data = json.load(sys.stdin)
tool_name = data.get("tool_name", "")
command = data.get("tool_input", {}).get("command", "")
```

### Output

Hooks communicate back to Harness via **exit code** and `stdout`/`stderr`. The
simplest way to do this is to return an error code and print additional context
to stderr. For example:

```bash
# Here, error code 2 blocks the tool, using stderr as the reason:
if some_bad_condition; then
  echo "Blocked: reason here" >&2
  exit 2
fi
```

| Exit Code | Meaning                                                          |
| --------- | ---------------------------------------------------------------- |
| 0         | Success. Stdout is parsed as JSON (see fields below).            |
| 2         | **Block the tool.** Stderr is used as the deny reason (no JSON). |
| 49        | **Halt the turn.** Stderr is used as the halt reason (no JSON).  |
| Other     | Non-blocking error. Logged and ignored — the tool call proceeds. |

The difference between exit 2 and exit 49:

- **Exit 2** blocks the current tool call. The agent sees the error and can try
  something else.
- **Exit 49** halts the whole turn. The agent doesn't get to respond further;
  the user takes over. Use this when something is wrong enough that the agent
  shouldn't keep trying. 49 sits in an empty slice of the exit-code space —
  between the generic-error range (1-30), the BSD `sysexits.h` range (64-78),
  and the killed-by-signal range (128+) — so it can't be hit by accident.

That said, if you need more control, or if you need to rewrite input, you can
use JSON on stdout. Exit 0 and print a JSON object to provide context, update
the input, or still deny/halt with a reason:

```jsonc
{
  "version": 1, // Output envelope version. Optional; defaults to 1.
  "decision": "allow", // "allow", "deny", or null. Omit for no opinion.
  "halt": false, // If true, halts the turn entirely. Where the event
                  // supports halting; see [Events](#events).
  "reason": "LGTM", // Shown when denying or halting.
  "context": "Scrubbed secrets", // String or array of strings. Appended to what the model sees.
  "updated_input": { "command": "…" }, // PreToolUse only. Shallow-merged into the tool's input.
  "updated_prompt": "…", // UserPromptSubmit only. Replaces the outbound prompt.
}
```

`version` is an optional integer at the top of the envelope. It defaults to `1`
if omitted. Unknown higher versions are still parsed; the field exists so the
envelope can evolve without a compatibility shim.

Only `deny` changes what happens. Harness runs every tool call it is not told
to block, so `decision: "allow"` and silence (no `decision`, or
`decision: null`) are equivalent: the tool runs either way. `"allow"` is still
accepted so existing hooks keep parsing, and it remains meaningful in
aggregation — it does not override another hook.s `deny`.

`updated_input` is a shallow-merge patch. Keys you include overwrite matching
keys in `tool_input`; keys you don't include are preserved. If the model called
`bash` with `{"command": "npm test", "timeout": 60000}` and your hook returns
`{"updated_input": {"command": "bun test"}}`, the tool runs with
`{"command": "bun test", "timeout": 60000}` — the timeout isn't dropped. The
merge is shallow: nested objects are replaced wholesale, not deep-merged.

`halt: true` stops the turn entirely. The agent doesn't get to respond further;
the user takes over. The exit-code shorthand is `exit 49` with stderr as the
reason.

`context` accepts either a string or an array of strings. Use the string form
for a single observation; use the array form when a hook produces multiple
distinct notes and you'd rather not concatenate them by hand. Empty strings and
empty array entries are dropped.

Here's a full shell script that produces this JSON:

```bash
#!/usr/bin/env bash
# Example: rewrite a bash command using RTK

read -r input
original_cmd=$(echo "$input" | jq -r '.tool_input.command')
rewritten=$(secret-scrubber rewrite "$original_cmd")

cat <<EOF
{
  "decision": "allow",
  "context": "Scrubbed secrets",
  "updated_input": {"command": "$rewritten"}
}
EOF
```

### Multiple Hooks

Hooks run in parallel, but their results compose in config order. Whichever hook
finishes first doesn't get to "win" by virtue of timing; composition is
deterministic based on the order hooks appear in the config.

When multiple hooks match the same tool call:

- If **any** hook denies, the tool call is blocked. `reason` values are
  concatenated in config order (newline-separated).
- If **any** hook halts, the turn ends after the tool call is blocked.
- If no hook denies or halts, the tool call proceeds.
- `context` values are concatenated in config order. Strings and arrays compose
  uniformly — each string becomes one entry, and array entries are flattened in.
- `updated_input` patches shallow-merge in config order against the original
  tool input. Later hooks override earlier ones on colliding keys. If denied or
  halted, `updated_input` patches are ignored.

### Timeouts

If a hook exceeds its timeout, Harness cancels its context and treats the
result as a non-blocking error so the tool call proceeds. The default
timeout is 30 seconds. Shebang-dispatched subprocesses are killed through
`exec.CommandContext`; in-process hooks get a short grace period to yield
and are then abandoned (the agent moves on regardless). Long-running work
should honor context cancellation or run out-of-process via a shebang.

## Examples

### Block destructive commands

Prevent the agent from running `rm -rf` in bash:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "^bash$",
        "command": "./hooks/no-rm-rf.sh"
      }
    ]
  }
}
```

`hooks/no-rm-rf.sh`:

```bash
#!/usr/bin/env bash
# Block rm -rf commands in the bash tool. Otherwise stay silent so the
# command runs.

if echo "$HARNESS_TOOL_INPUT_COMMAND" | grep -qE 'rm\s+-(rf|fr)\s+/'; then
  echo "Refusing to run rm -rf against root" >&2
  exit 2
fi

exit 0
```

### Inject context into file writes

Add a reminder to the model whenever it writes a Go file:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "^(edit|write|multiedit)$",
        "command": "./hooks/go-context.sh"
      }
    ]
  }
}
```

`hooks/go-context.sh`:

```bash
#!/usr/bin/env bash
# Remind the model about Go formatting when editing .go files.
# Emit context only; stay silent on `decision` so the edit still runs.

if [[ "$HARNESS_TOOL_INPUT_FILE_PATH" == *.go ]]; then
  echo '{"context": "Remember: run gofumpt after editing Go files."}'
else
  echo '{}'
fi
```

### Block all MCP tools

The `command` can be inline. This one-liner matches all MCP tools and blocks
them:

```yaml
- matcher: "^mcp_"
  command: echo 'MCP tools are disabled' >&2; exit 2
```

### Log every tool call

With no `matcher` this fires for every tool. It exits 0 with no stdout so the
tool call always proceeds.

```yaml
- command: echo "$(date -Iseconds) $HARNESS_TOOL_NAME" >> ./tools.log
```

### A real-world Example:

For a more practical example, see [`rtk-rewrite.sh`](./examples/rtk-rewrite.sh),
which demonstrates how to rewrite tool input using
[RTK](https://github.com/rtk-ai/rtk) to save tokens.

### Using other languages

Hooks aren't limited to shell scripts: any executable works. Here's the same
"block rm -rf" example in some other languages.

#### Lua

`{"matcher": "^bash$", "command": "lua ./hooks/no-rm-rf.lua"}`

```lua
local input = io.read("*a")
local tool_input = input:match('"command":"(.-)"') or ""

if tool_input:match("rm%s+%-[rf][rf]%s+/") then
  io.stderr:write("Refusing to run rm -rf against root\n")
  os.exit(2)
end
```

#### JavaScript

`{"matcher": "^bash$", "command": "node ./hooks/no-rm-rf.js"}`

```js
let input = "";
process.stdin.on("data", (chunk) => (input += chunk));
process.stdin.on("end", () => {
  const { tool_input: toolInput } = JSON.parse(input);

  if (/rm\s+-[rf]{2}\s+\//.test(toolInput.command)) {
    process.stderr.write("Refusing to run rm -rf against root\n");
    process.exit(2);
  }
});
```

---

## Claude Code compatibility

Harness hooks are broadly compatible with [Claude Code
hooks](https://docs.claude.com/en/docs/claude-code/hooks): the config shape,
stdin payload, output envelope, and exit codes line up so most Claude Code
hooks run under Harness unchanged. This document covers the Harness-specific API
only — anything not documented here isn't guaranteed to work.

One intentional divergence: Harness treats `updated_input` as a shallow-merge
patch against the original `tool_input` rather than a full replacement. Keys
you omit are preserved. See [Output](#output) for details.

---

## Reference

This is the official reference of the narrative above. If prose and this section
disagree, the prose should be presumed canonical for intent, while this section
is canonical for shape.

Both the stdin payload and the output envelope have **common fields** that apply
to every event and **per-event fields** that only some events recognize. When an
event doesn't understand a field, it's ignored.

### Hook config

Each entry in a `hooks.<EventName>` list:

```yaml
# string. Optional. Friendly display name shown in the TUI. Falls back to
# command when omitted.
- name: no-rm-rf

  # string. Optional. Regex tested against the event's subject: the tool
  # name for tool events, the sub-agent type for SubagentStop. Omit to
  # match all. Events without a subject ignore it (an empty matcher is
  # the only thing that matches them).
  matcher: "^bash$"

  # string. Required. Shell command to run.
  command: ./hooks/my-hook.sh

  # number. Optional. Seconds before the hook is killed. Defaults to 30.
  timeout: 10
```

### Stdin payload (common)

Present in every hook event:

```jsonc
{
  // string. Hook event name.
  "event": "PreToolUse",

  // string. Current session ID.
  "session_id": "313909e",

  // string. Working directory when invoked.
  "cwd": "/home/user/project",
}
```

### Stdin payload — PreToolUse

Extends the common payload:

```jsonc
{
  // ...common fields...

  // string. The tool being called.
  "tool_name": "bash",

  // object. Raw JSON input the model sent to the tool. Shape is per-tool.
  "tool_input": {
    "command": "npm test",
  },
}
```

### Stdin payload — PostToolUse

Extends the common payload:

```jsonc
{
  // ...common fields...

  // string. The tool that ran.
  "tool_name": "bash",

  // object. The input the tool ran with (after any PreToolUse rewrite).
  "tool_input": { "command": "npm test" },

  // object. The completed tool call's outcome.
  "tool_response": {
    "content": "all tests passed",
    "is_error": false,
  },
}
```

### Stdin payload — UserPromptSubmit

```jsonc
{
  // ...common fields...

  // string. The prompt as the user typed it.
  "prompt": "fix the login flow",

  // string[]. Attachment file names, when the prompt carried any.
  "attachments": ["screenshot.png"],
}
```

### Stdin payload — SubagentStop

```jsonc
{
  // ...common fields...

  // string. The dispatched sub-agent type ("task", "fast", or custom).
  "subagent_type": "fast",

  // string. Final status: "completed", "cancelled", or "failed".
  "message": "completed",
}
```

### Stdin payload — PreCompact / PostCompact

```jsonc
{
  // ...common fields...

  // string. "auto" (context window threshold) or "manual" (/summarize).
  "trigger": "auto",
}
```

### Stdin payload — Notification

```jsonc
{
  // ...common fields...

  // string. One of "agent_finished", "error", "agent_retrying".
  "notification_type": "agent_finished",

  // string. Human-readable message (may be empty).
  "message": "",
}
```

`SessionStart` and `Stop` carry only the common fields (plus `prompt` on
`SessionStart`).

### Output envelope (common)

Fields a hook may print to stdout on exit 0. All are optional and apply to every
event:

```jsonc
{
  // number. Defaults to 1. Unknown higher values still parse; exists for
  // forward-compat.
  "version": 1,

  // boolean. If true, ends the turn entirely. User takes over.
  "halt": false,

  // string. Shown when denying (to the model) or halting (to the model and
  // user).
  "reason": "not allowed",

  // string | string[]. Appended to what the model sees. Empty entries are
  // dropped.
  "context": "Rewrote with RTK",
}
```

### Output envelope — PreToolUse

Extends the common envelope:

```jsonc
{
  // ...common fields...

  // "allow" | "deny" | null. Only "deny" changes anything: it blocks the call
  // and the model sees the error and may try something else. null/omitted and
  // "allow" both let the tool run.
  "decision": "deny",

  // object. Shallow-merge patch against tool_input. Nested objects are
  // replaced wholesale, not deep-merged.
  "updated_input": {
    "command": "bun test",
  },
}
```

### Output envelope — UserPromptSubmit

Extends the common envelope:

```jsonc
{
  // ...common fields...

  // "deny" (or halt: true) blocks the submission: the turn never reaches
  // the model and the reason becomes the run error the user sees.
  "decision": "deny",

  // string. Full replacement for the outbound prompt — not a merge patch.
  // The stored message keeps the original; only the model sees the rewrite.
  "updated_prompt": "fix the login flow (clarified)",
}
```

### Output envelope — other events

`PostToolUse`, `SessionStart`, `Stop`, `SubagentStop`, `Notification`, and
`Pre`/`PostCompact` use the common envelope only. On `PostToolUse` a `deny`
appends the reason to the tool response as feedback instead of blocking; on
the informational events decisions are logged and `context` is delivered as
documented under [Events](#events).

### Exit codes

| Code  | Meaning                                                                  |
| ----- | ------------------------------------------------------------------------ |
| `0`   | Success. Stdout is parsed as the output envelope.                        |
| `2`   | Block this tool call. Stderr becomes the deny reason. Stdout is ignored. |
| `49`  | Halt the whole turn. Stderr becomes the halt reason. Stdout is ignored.  |
| other | Non-blocking error. Logged and ignored; the tool call proceeds.          |

Exit `2` only applies to events that can block something (`PreToolUse` blocks
the tool call, `UserPromptSubmit` blocks the turn, `PostToolUse` downgrades to
feedback). On purely informational events it's treated as a non-blocking
error.

### Aggregation

When multiple hooks match the same event, results compose in **config order**.

Universal rules:

1. `halt` is sticky: if any hook halts, the turn ends.
2. `reason` values concatenate with `\n` in config order. Halt-only hooks
   without a deny still contribute their reason.
3. `context` values concatenate with `\n` in config order. String entries and
   array entries flatten uniformly.

PreToolUse-specific rules:

4. `decision` precedence: `deny` > `allow` > `null`. First deny determines the
   outcome; subsequent allows don.t override. Any final decision other than
   `deny` lets the tool run.
5. `updated_input` patches shallow-merge sequentially against the original
   `tool_input`. Later patches override earlier ones on colliding keys. Patches
   are **ignored** if the final decision is deny or halt.

UserPromptSubmit-specific rules:

6. `updated_prompt` is a full replacement, and the **last** hook in config
   order wins. A final decision of deny or halt discards any rewrite.

### Environment variables

See [Environment Variables](#environment-variables) above for the full list.

