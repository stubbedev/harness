# Extensions

> [!NOTE]
> This document was designed for both humans and agents.

Extensions are Lua programs that run **inside** Harness. Where a hook is a
shell command Harness shells out to, and an MCP server is another process on
the other end of a protocol, an extension is loaded into the binary itself: it
registers agent tools, hook handlers and slash commands, and they behave like
the built-in ones.

The VM is [gopher-lua](https://github.com/yuin/gopher-lua) — pure Go, so
`CGO_ENABLED=0` still holds and there is nothing to install. The language is
Lua 5.1.

### Hot Extension Facts

- An extension is a directory with an `init.lua` in it
- It can register **tools** the model can call, **hook handlers** for the same
  nine events hooks fire on, and **commands** that appear in the palette
- The VM starts empty: no `io`, no `debug`, no `os.execute`, no `dofile`.
  Everything an extension can reach is a function on the `harness` table
- Anything that touches the machine — files, the shell, the network — fires a
  `PreToolUse` hook first, so an existing hook policy still polices it
- `require` resolves inside the extension's own directory, so an extension can
  be more than one file
- One extension failing to load never stops the others

## Baby's First Extension

```
~/.config/harness/extensions/
  wordcount/
    init.lua
```

```lua
-- ~/.config/harness/extensions/wordcount/init.lua

harness.register_tool({
  name = "wordcount",
  description = "Counts the words in a file in the workspace.",
  parameters = {
    path = { type = "string", description = "Path to the file", required = true },
  },
  handler = function(input)
    local content = harness.fs.read(input.path)
    if content == nil then
      return { content = "no such file: " .. input.path, is_error = true }
    end

    local words = 0
    for _ in content:gmatch("%S+") do
      words = words + 1
    end
    return input.path .. ": " .. words .. " words"
  end,
})
```

Start Harness and ask it to count the words in a file. That's it — no config
to write, no process to start.

## Where extensions live

Every directory below is scanned, in order. A directory containing an
`init.lua` is an extension, and its **directory name is the extension's name**.

| Scope   | Path                                               |
| ------- | -------------------------------------------------- |
| Global  | `~/.config/harness/extensions/`                     |
| Global  | `~/.config/agents/extensions/`                      |
| Project | `<repo root>/.harness/extensions/`                  |
| Project | `<repo root>/.agents/extensions/`                   |
| Project | `<working dir>/.harness/extensions/`                |
| Project | `<working dir>/.agents/extensions/`                 |

Later paths win: a project extension shadows a global one of the same name.

Add your own paths, or turn one off, in the config:

```yaml
options:
  extensions_paths:
    - ~/src/harness-extensions
  disabled_extensions:
    - wordcount
```

`HARNESS_EXTENSIONS_DIR`, when set, replaces the global paths entirely.

Names are letters and digits separated by single dashes or underscores, at
most 64 characters. A directory with any other name is skipped and the reason
is reported (see [Diagnostics](#diagnostics)).

## What an extension can register

All three registrations happen **while `init.lua` runs**. The tool list, the
hook set and the command list are read once, when loading finishes; a
registration attempted later raises an error.

### Tools

```lua
harness.register_tool({
  name = "jira_issue",              -- required, unique across all tools
  description = "Fetch a Jira issue by key.",
  parallel = false,                 -- may the model run it alongside others?
  parameters = {
    key = { type = "string", description = "Issue key, e.g. HAR-24", required = true },
    fields = { type = "array", items = { type = "string" }, description = "Fields to return" },
  },
  handler = function(input, ctx)
    -- input is the model's arguments, already decoded
    -- ctx carries { tool_call_id, session_id, extension, working_dir }
    return "..."
  end,
})
```

`parameters` takes either the shorthand above (a table of parameter name to
its spec, with `required = true` on the ones that are mandatory) or a JSON
Schema object, recognised by its `properties` key:

```lua
  parameters = {
    type = "object",
    properties = { key = { type = "string" } },
    required = { "key" },
  },
```

A handler returns either a string, or a table:

```lua
return {
  content = "what the model sees",
  is_error = false,   -- true renders it as a failed tool call
  stop_turn = false,  -- true ends the turn after this result
  metadata = { any = "json-able table" },
}
```

An error raised inside a handler (`error("nope")`, or a failed host call) comes
back to the model as a tool error it can read and recover from — it does not
fail the turn.

Extension tools bypass an agent's `allowed_tools` list, exactly as MCP tools
do: their names cannot be known when that list is written, and installing the
extension is itself the opt-in.

### Hook handlers

`harness.on` registers an in-process handler for any of the nine hook events
(`PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `SessionStart`, `Stop`,
`SubagentStop`, `Notification`, `PreCompact`, `PostCompact`). See
[the hooks documentation](../hooks/README.md) for what each event carries.

```lua
-- The optional second argument is a regex matched against the event's
-- subject: the tool name, or the sub-agent type for SubagentStop.
harness.on("PreToolUse", "^shell$", function(event)
  if event.tool_input.command:find("rm %-rf") then
    return { decision = "deny", reason = "not on my watch" }
  end
end)

harness.on("UserPromptSubmit", function(event)
  return { updated_prompt = event.prompt .. "\n\nBe brief." }
end)
```

The payload is the same JSON a shell hook reads on stdin, decoded into a
table. What a handler returns is its verdict:

| Return                              | Meaning                                       |
| ----------------------------------- | --------------------------------------------- |
| `nil` (or nothing)                  | no opinion                                    |
| a string                            | added to the model's context                  |
| `{ decision = "deny", reason = … }` | block the call; the reason goes to the model   |
| `{ decision = "allow" }`            | explicitly allow                               |
| `{ halt = true, reason = … }`       | stop the whole turn                            |
| `{ context = … }`                   | add to the model's context                     |
| `{ updated_input = { … } }`         | shallow-merge a patch over the tool's input    |
| `{ updated_prompt = … }`            | replace the user's prompt                      |

Lua handlers and shell hooks are aggregated together, so a deny from either
denies, and both appear in the hook indicator in the UI.

### Commands

```lua
harness.register_command({
  name = "standup",
  description = "Draft today's standup from the git log",
  handler = function(args)
    local log = harness.exec("git log --since=yesterday --oneline --author=$(git config user.email)")
    return "Write a standup note from these commits:\n\n" .. log.stdout
  end,
})
```

The command appears in the palette as `ext:<extension>:<command>` — here
`ext:standup:standup`. What the handler returns is sent as the user's message.

A command that needs no code can declare a static prompt and the arguments the
palette should ask for; `$NAME` placeholders are substituted:

```lua
harness.register_command({
  name = "review",
  description = "Review a file",
  prompt = "Review $FILE for correctness bugs.",
  arguments = {
    { id = "FILE", title = "File", description = "Path to review", required = true },
  },
})
```

Handler-backed commands receive the same arguments as a table
(`args.FILE`).

## The `harness` table

Everything the VM can reach, in one place.

| Function                                   | Returns                                             |
| ------------------------------------------ | --------------------------------------------------- |
| `harness.name`, `harness.dir`               | this extension's name and directory                 |
| `harness.version`                           | the Harness version string                          |
| `harness.register_tool(spec)`               | —                                                    |
| `harness.register_command(spec)`            | —                                                    |
| `harness.on(event[, matcher], handler)`     | —                                                    |
| `harness.log.debug/info/warn/error(msg[, fields])` | —                                            |
| `harness.json.encode(value)` / `.decode(s)` | string / value                                      |
| `harness.env(name)`                         | the environment variable, or `nil`                  |
| `harness.workspace()`                       | `{ root, data_dir, extension_dir }`                 |
| `harness.fs.read(path)`                     | contents, or `nil, err`                             |
| `harness.fs.write(path, content)`           | `true`, or `false, err` (creates parent dirs)       |
| `harness.fs.append(path, content)`          | `true`, or `false, err`                             |
| `harness.fs.list(dir)`                      | array of `{ name, dir }`, or `nil, err`             |
| `harness.fs.exists(path)`                   | boolean                                             |
| `harness.fs.mkdir(path)`                    | `true`, or `false, err`                             |
| `harness.exec(command[, opts])`             | `{ stdout, stderr, code, ok }`                      |
| `harness.http.get(url[, headers])`          | `{ status, ok, body, headers }`, or `nil, err`      |
| `harness.http.post(url, body[, headers])`   | same                                                |
| `harness.http.request(spec)`                | same; `spec` is `{ url, method, headers, body }`    |

Relative paths resolve against the workspace root. `harness.exec` runs through
the same embedded POSIX shell the shell tool and hooks use, takes
`{ cwd = "...", stdin = "..." }`, and never raises on a non-zero exit — check
`result.code`. A table `body` for `harness.http` is encoded as JSON.

## The sandbox

The VM is opened with `base`, `table`, `string`, `math`, `coroutine`, `os` and
`package` only. There is no `io` library and no `debug` library; `os.execute`,
`os.exit`, `os.remove`, `os.rename` and `os.tmpname` are removed, as are
`dofile` and `loadfile`. `package.path` points at the extension's own directory
and `package.cpath` is empty.

What remains is the `harness` table, which is the point: every capability is a
host function, so what extensions can do is auditable in one file
(`internal/extensions/api_host.go`).

### Extensions are not more trusted than hooks

An extension you install can read your files, run commands and reach the
network — like a hook, and like any program you run. What it cannot do is
bypass your hook policy: `harness.fs.read`, `harness.fs.write`,
`harness.exec` and `harness.http` each fire a `PreToolUse` hook first, under a
synthetic tool name:

| Host function                            | Tool name in the hook payload |
| ---------------------------------------- | ----------------------------- |
| `harness.fs.read`, `harness.fs.list`     | `extension_read`              |
| `harness.fs.write/append/mkdir`          | `extension_write`             |
| `harness.exec`                           | `extension_exec`              |
| `harness.http.*`                         | `extension_http`              |

A hook that denies one of these turns it into an error inside the script:

```yaml
hooks:
  PreToolUse:
    - matcher: "^extension_exec$"
      command: "jq -e '.tool_input.command | test(\"^git \")' >/dev/null || exit 2"
```

Only shell hooks police these calls. A Lua hook handler does not, because it
would mean re-entering a VM that is already running.

## Limits worth knowing

- **One call at a time.** Each extension has one VM, and calls into it are
  serialised. Two tools from the same extension never run in parallel; tools
  from different extensions do.
- **30 seconds per call.** A handler that runs longer is cancelled and the
  model is told so.
- **Registration is load-time.** See above.
- **Tool names are global.** The first extension to register a name keeps it;
  a later collision is dropped with a warning. Avoid the built-in tool names.
- **No state between calls beyond the VM.** Globals in `init.lua` persist for
  the process lifetime. To keep anything longer, write it under
  `harness.workspace().data_dir`.
- **Reloading means restarting.** Extensions load once at startup.

## Diagnostics

Ask Harness about itself — the `harness` tool's `state` action has an
`[extensions]` section listing every extension, what it registered, and the
error for any that failed:

```
[extensions]
jira: loaded tools=jira_issue commands=ext:jira:triage hooks=PreToolUse
wordcount: error error="load extension \"wordcount\": ..."
noisy: disabled
```

Anything an extension logs with `harness.log` lands in the Harness log
(`harness logs`, or the `harness` tool's `logs` action) tagged with the
extension's name.

## Extensions, hooks, MCP, or a skill?

- A **skill** is instructions: it tells the model how you want something done.
- A **hook** is policy and glue at the process boundary, in whatever language
  you like, on the nine lifecycle events.
- An **MCP server** is a separate program, usually someone else's, speaking a
  standard protocol — the right answer for a real integration with its own
  dependencies and release cycle.
- An **extension** is for the small, specific thing you want Harness itself to
  be able to do: a tool wrapping your team's API, a guard on a tool call, a
  command that assembles a prompt from the repo. No process, no protocol, no
  startup cost.

## Examples

Two runnable extensions live in [`examples/`](./examples):

- [`no-force-push`](./examples/no-force-push/init.lua) — a `PreToolUse`
  handler that blocks `git push --force` and names the safer flag.
- [`todos`](./examples/todos/init.lua) — a tool, a command and a hook handler
  in one file, built around `rg`.

Copy either directory into `~/.config/harness/extensions/` and restart.
