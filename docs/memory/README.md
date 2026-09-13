# Memory

Harness lets the agent maintain its own durable memory across sessions, on top
of the static context files (`AGENTS.md` / `HARNESS.md`), which you author.
Context files are static and user-owned; memory is dynamic and agent-owned.

## Storage

Memories live in the workspace SQLite database (in the workspace data directory
under the global data root, or your `options.data_directory`) in a `memories` table, alongside sessions. That gives
per-project scoping and cross-session durability for free: a new session in the
same workspace sees everything saved before. No markdown files are written into
your repo, and nothing memory-related needs gitignoring.

## How it works

1. **Prompt steering.** While memory is enabled, the coder system prompt carries a `# Memory` block telling the agent what is worth saving (user preferences, corrections, non-obvious project facts, decisions and their rationale, recurring patterns), to save the moment it learns something rather than batching to the end, and what to keep out (rediscoverable facts, secrets). The block renders even on an empty store, so a fresh workspace is steered from the first session.
2. **Index injection.** The block also carries a compact index (one line per memory: id, category, title), refreshed at the start of every turn and bounded by `options.memory.index_budget` characters (default 4000). Saving or deleting a memory is reflected on the next turn.
3. **The `memory` tool.** The agent reads full notes on demand and maintains
   the store via five actions: `save`, `read`, `search`, `list`, `delete`.
   Saving without an id upserts by title, so re-saving the same title updates
   the note instead of duplicating it.

### Categories

| Category    | Holds                                                              |
| ----------- | ------------------------------------------------------------------ |
| `user`      | Stable facts about the user: preferences, environment, habits      |
| `feedback`  | Corrections and guidance that should shape future behavior          |
| `project`   | Non-obvious codebase facts not in context files or easily rediscovered |
| `reference` | Pointers to external material: docs, issues, discussions, repos     |

## Reaping

The store is capped at `options.memory.max_memories` (default 500). When a save
exceeds the cap, the least useful memories are deleted: lowest use count first,
then least recently used, then oldest. Every `read` or `search` hit touches a
memory's usage counters. Pinned memories (`pinned: true` on save) are never
reaped.

## Privacy

Content is scrubbed before anything is persisted. Common secret shapes are
redacted: provider API keys (`sk-ant-...`, `sk-proj-...`, `AKIA...`,
`AIza...`), GitHub and Slack tokens, `Bearer` headers, PEM private key blocks,
and `password: ...` / `api_key = ...` style assignments. The agent is told in
the tool response when a redaction happened. This is a safety net, not a
guarantee — never ask the agent to memorize credentials.

## Sub-agents

By default only the orchestrator (coder agent) has the `memory` tool; the
built-in `task` and `fast` sub-agents resolve to a subagent tool set that
excludes it. A custom subagent definition can opt in by listing `memory` in its
`tools` frontmatter; it then shares the workspace store with the orchestrator.

## Disabling

```yaml
options:
  memory:
    enabled: false
```

This removes the tool and the prompt injection entirely. The database rows are
left untouched.
