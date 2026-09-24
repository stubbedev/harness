# Deterministic skill activation

Skills without `activation` retain existing behavior: invoke their `/` command,
or find and load them with `skill_search`. Descriptions remain semantic search
metadata; Harness never classifies user prose to decide automatic activation.

Add optional frontmatter to `SKILL.md`:

```yaml
---
name: go-maintenance
description: Procedures for maintaining Go code.
activation:
  paths:
    - "**/*.go"
    - "go.mod"
  directories:
    - internal/agent
  tools:
    - name: lsp
      actions: [rename, references]
    - name: edit
  capabilities: [go]
  markers: [.project-go-maintenance]
---
Your procedure goes here.
```

## Matching

All lists and categories are **OR** alternatives: any matching rule activates
the skill. In one tool rule, the tool name **and** an action must match; omitting
`actions` allows any action for that tool. Names and actions are exact,
case-sensitive identifiers, not fuzzy search terms. Do not put a broad project
rule alongside a narrow file rule expecting an AND condition.

- `paths`: workspace-relative slash-separated globs. `*` matches within one
  component; `**` matches zero or more directories. Quote YAML globs. Absolute
  tool paths are normalized relative to the workspace; paths outside it do not
  match.
- `directories`: literal workspace-relative directory names, including their
  descendants. `internal/agent` does not match `internal/agents`. `.` matches
  every workspace path.
- `tools`: exact tool `name`, optionally restricted to exact `actions`. The
  action comes only from a structured tool argument named `action`.
- `capabilities`: exact project capability names supplied by the host or
  detected from these root marker files:

  | Capability | Any one of these files |
  | --- | --- |
  | `go` | `go.mod`, `go.work` |
  | `node` | `package.json` |
  | `python` | `pyproject.toml`, `requirements.txt`, `setup.py` |
  | `rust` | `Cargo.toml` |
  | `ruby` | `Gemfile` |
  | `java` | `pom.xml`, `build.gradle`, `build.gradle.kts` |
  | `dotnet` | `global.json` |

- `markers`: literal workspace-relative regular files. No globbing or content
  interpretation. Symlinks escaping the workspace do not count.

An activation block must contain at least one rule. Invalid globs, absolute
rule paths, parent traversal, empty names, and empty actions are rejected by
skill validation. Omit the block entirely to opt out.

## Timing and boundaries

The main agent checks project rules before its first model request and checks
structured tool calls before each subsequent model request. File evidence comes
from `file_path`, `path`, `working_dir`, or `files[].file_path` arguments.
Shell command text, tool output prose, descriptions, and user prose are not
classified or executed to infer activation.

Tool-driven activation cannot affect a tool call already executed: its
instructions arrive before the **next model step**. For instructions required
before the first action, use an applicable project rule, explicitly load the
skill, or invoke its existing `/` command.

Only active skills can activate. `disable-model-invocation: true` always blocks
automatic loading, even when a rule matches. Explicit user invocation remains
unchanged. Custom subagents keep their existing declared-skill behavior;
automatic activation is not enabled for them by the coordinator. The standalone
API supports an explicit allowed-name set: nil is unrestricted, an empty set
denies all, and a nonempty set permits only listed active skills.

Successful automatic loads are recorded in the existing skill tracker. Skills
are considered in name order and loaded at most once per run, keyed by name and
source path, with a maximum of 32 skills and 128 KiB of rendered instructions.
Bodies exceeding the remaining budget are skipped without marking them loaded;
explicit loading remains available. Load failures are logged and not marked.
Only the latest 256 structured tool events are considered. Loaded bodies are
kept available to subsequent model steps without duplicate copies when already
present in the request. The session tracker is diagnostic state, not a global
suppression mechanism, so one session never prevents another from loading a
skill.
