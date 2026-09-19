# Directory instructions

Harness discovers project instructions when a built-in file tool accesses a path.
Instructions are loaded from the workspace root down to the accessed directory,
never from ancestors above the workspace. Each body names its file and directory
scope. Deeper instructions take precedence within that scope; instructions for a
sibling directory do not apply to other siblings.

Discovery uses the established `AGENTS.md`, `HARNESS.md`, `CLAUDE.md`, `GEMINI.md`
filenames, their existing case and local variants, `.cursorrules`,
`.github/copilot-instructions.md`, and relative file entries in `context_paths`.
Configured absolute paths, glob patterns, parent escapes, and directory entries
are not recursively expanded. Missing files are ignored. This does not search
all descendants before an operation.

## Tool behavior

- `view`: inspects `file_path` or every entry in `files`. Reads execute normally;
  newly discovered instructions accompany the tool result.
- `edit` and `write`: inspect `file_path`. If an applicable instruction version
  has not reached a model step, the mutation does not execute. The result includes
  the instructions and explicitly requires a retry on the next model turn.
- `lsp`: uses the structured path consumed by the selected action (`file_path`
  for symbols, diagnostics and replace-symbol; `path` for definition, references,
  call hierarchy and rename). Rename and replace-symbol use the mutation retry
  rule. This cannot predict additional files affected by a language-server rename.
- `shell`: inspects only an explicit `working_dir`. Shell commands, prose,
  redirects, `cd`, and persistent terminal state are not parsed. Shell execution
  is not guarded as a file mutation.
- MCP and unrelated tools are not inspected. Batch execution must resolve to the
  wrapped built-in tools rather than parsing arbitrary batch or MCP payloads.

This is instruction delivery, not a permission mechanism or security sandbox.
Existing approvals and hooks are unchanged. Symlinks must not cause discovery to
read instructions outside the workspace. Instruction read errors are reported;
mutations stop when their applicable instructions cannot be loaded.

## Session lifecycle and limits

Versions use content hashes rather than timestamps. Each session independently
tracks active instructions and whether the model has seen each version. Parallel
calls cannot unlock a first mutation simply by discovering the same instructions
in another tool call. Only the next model-step preparation acknowledges them.
Changed files are reloaded. Removed files stop being replayed.

Active instruction bodies are replayed into ephemeral model input if absent from
conversation history, including after compaction. Tool-result bodies are not
injected again when the exact version is already present. Old versions can remain
in historical messages, but the current version is delivered again.

The per-file limit is 32 KiB. Active framed bodies share a 96 KiB budget, with a
maximum of 128 files per session. Oversized files or instruction sets produce an
explicit error rather than silently truncating instructions; affected mutations
do not execute. These limits concern instructions, not ordinary tool output.

## Integration API

```go
tracker := NewDirectoryInstructions(workspaceRoot, contextPaths)
tracker.ExcludePromptPaths(alreadyLoadedPromptFiles)
builtins = tracker.WrapTools(builtins)
prepared.Messages = tracker.Prepare(callContext, prepared.Messages)
```

`ExcludePromptPaths` is optional: it snapshots exact file versions already loaded
in the system prompt. Changed versions are still discovered. Pass actual loaded
files, not directories or files omitted by a prompt budget.

Wrap built-ins before wrapping them with tool hooks so rewritten hook inputs are
checked and ordinary calls still pass through the existing hooks. Batch resolvers
must receive these wrapped tools. The wrapper preserves tool metadata, provider
options, and MCP server identity.

Call `Prepare` on every model step, with the same session-ID context used for tool
calls, after compaction and before sending model input. Its returned messages are
ephemeral; they need not be persisted. A tracker can serve multiple sessions, but
must be constructed with the actual root of each isolated subagent worktree, not
the parent workspace. Reuse the tracker for the lifetime of its session tools.
