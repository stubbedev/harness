# Memory

Harness lets the agent maintain its own durable memory across sessions, on top
of the static context files (`AGENTS.md` / `HARNESS.md`), which you author.
Context files are static and user-owned; memory is dynamic and agent-owned.

## Storage

Memories live in one SQLite database shared by every workspace and every
Harness process on the machine: `memory/memory.db` under the global data root
(`~/.local/share/harness`, `$XDG_DATA_HOME/harness` or `$HARNESS_GLOBAL_DATA`).
No markdown files are written into your repo, and nothing memory-related needs
gitignoring.

### Scopes

Each memory has a scope:

| Scope    | Seen from                                                         |
| -------- | ----------------------------------------------------------------- |
| `global` | Every workspace                                                   |
| `repo`   | Every clone, worktree and subdirectory of one repository          |

A new memory's scope follows its category unless the save names one: `user` and
`feedback` memories are global, `project` and `reference` memories belong to the
repository. The same title can exist once in each scope; a read, edit or delete
that names no scope picks the repository's, and says so.

A repository is identified by its upstream, not its path, so another clone, a
worktree, or the same repository on another machine shares its memories. The
key is the URL of the `origin` remote (else the first remote by name) reduced
to host and path: `git@github.com:owner/repo.git`, `https://github.com/owner/repo`
and `ssh://git@github.com/owner/repo` are all `github.com/owner/repo`. Scheme,
user info, port and a trailing `.git` are dropped, and the path is lowercased
for GitHub, GitLab and Bitbucket, which ignore its case. A repository without a
remote is keyed by its root commit, and a directory outside any repository by
its path.

## How it works

1. **Prompt steering.** While memory is enabled, the coder system prompt carries a `# Memory` block telling the agent what is worth saving (user preferences, corrections, non-obvious project facts, decisions and their rationale, recurring patterns), to save the moment it learns something rather than batching to the end, and what to keep out (rediscoverable facts, secrets). The block renders even on an empty store, so a fresh workspace is steered from the first session.
2. **Index injection.** The block also carries a compact index (one line per memory: category and title, global memories first, then the repository's), refreshed at the start of every turn and bounded by `options.memory.index_budget` characters (default 4000). Each scope is guaranteed half the budget, and the share one does not use goes to the other. Saving or deleting a memory is reflected on the next turn.
3. **The `memory` tool.** The agent reads full notes on demand and maintains
   the store via six actions: `save`, `edit`, `read`, `search`, `list`, `delete`.
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

Each scope is capped at `options.memory.max_memories` (default 500): the global
memories, and each repository's. When a save exceeds its scope's cap, the least
useful memories of that scope are deleted: lowest use count first, then least
recently used, then oldest. A busy repository therefore never evicts global
memories or another repository's. Every `read` or `search` hit touches a
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
`tools` frontmatter; it then shares the store with the orchestrator.

## Disabling

```yaml
options:
  memory:
    enabled: false
```

This removes the tool and the prompt injection entirely. The database rows are
left untouched.
