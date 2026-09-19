# Subagent worktree isolation

Custom subagent Markdown frontmatter can opt into a separate checkout:

```yaml
---
name: isolated-editor
description: Make an independently reviewable change.
isolation: worktree
---
Implement the requested change and describe the result.
```

`isolation` accepts `worktree` or an empty/omitted value. Omission retains
shared-checkout behavior. The agent tool also accepts `isolation: "worktree"`
for an individual dispatch; the dispatch coordinator resolves that request
and the custom agent default before constructing the child's workspace.

Worktree isolation is a working-directory convenience, **not a sandbox or an
approval boundary**. Agents remain fully trusted. Git worktrees share repository
objects, configuration, and refs. Absolute paths, symlinks, shell commands, and
external tools can still access the original checkout or other files.

## Lifecycle

Each isolated dispatch creates a uniquely named `harness/harness-worktree-*`
branch from the source checkout's current commit, with its worktree in private
scratch storage outside the checkout. A caller can supply a session-data storage
directory; otherwise the system temporary directory is used.

The initial filesystem snapshot includes tracked, untracked, and ignored files,
including tracked deletions, binary contents, executable permissions, empty
directories, and symlinks. Git metadata is excluded. Symlinks are copied as links,
not traversed. The child starts with the source's current file contents, even if
the source has uncommitted changes. The source index is never modified, and its
staged/unstaged split is not reproduced: the child index initially reflects HEAD.
No reset, stash, commit, merge, automatic patch application, or push is performed.

The snapshot reads the entire checkout, including ignored files, except
directories whose names match a default exclusion set of well-known regenerated
dependency, build, and cache directories: `node_modules`, `vendor`, `target`,
`build`, `dist`, `out`, `bin`, `obj`, `.next`, `.nuxt`, `.output`, `.venv`,
`venv`, `__pycache__`, `.pytest_cache`, `.mypy_cache`, `.ruff_cache`, `.tox`,
`.gradle`, `.cache`, `.idea`, `.vscode`, `coverage`, `.turbo`, `.terraform`,
`Pods`, `.dart_tool`, `.parcel-cache`, `elm-stuff`, `_build`, `deps`,
`bower_components`, `cmake-build-debug`, and `cmake-build-release`. An excluded
directory is not copied into the baseline snapshot or the child, so the child
starts without it; package managers and build tools regenerate such
directories on demand. Exclusion applies only when Git does not track the
directory: a same-name directory with committed content (for example vendored
dependencies) is copied like any other content. Excluded paths stay outside the
snapshots that change detection compares, so results stay deterministic:
activity inside an excluded directory neither flags nor preserves the child,
and cleanup ignores it. The top-most excluded directories (relative,
slash-separated paths) are reported in the result's `excluded` list. Sensitive
ignored files outside the exclusion set still appear in the child snapshot: use
private storage. Checkouts with large unexcluded directories can still take
significant time and disk space. Nested repositories, submodules, special
files, unstable snapshots, non-Git folders, and repositories without a first
commit fail rather than risk an incomplete overlay; a nested repository inside
an excluded directory is skipped with the directory instead of failing. No
fallback to a shared checkout occurs.

After all child processes and tools have stopped, completion compares content
and permissions against the initial **dirty filesystem baseline**, and checks
HEAD, branch identity, and staged changes. An unchanged child worktree and its
unique branch are removed. Changes in ignored or untracked files also prevent
cleanup. Any uncertainty, inspection failure, or canceled completion preserves
storage instead of attempting destructive cleanup.

A changed worktree is retained. Results report its absolute path, branch, a
binary-capable `changes-*.patch` artifact, and the excluded regenerated
directories. That patch compares the child's final
filesystem to the initial dirty snapshot, not to HEAD. A separate `index-*.patch`
is reported when the index differs from the original HEAD, so staged-only edits
are not hidden by an unchanged working file. Neither patch is automatically
applied. The retained worktree and snapshot remain the authoritative inspection
sources: Git patches do not encode empty directories, arbitrary permission bits,
or branch-history-only changes. Applying patches with Git can invoke line-ending
conversion dictated by attributes; inspect raw snapshots when exact bytes matter.

Creation errors after storage allocation report the preserved scratch path,
checkout path, and branch, even when creation was incomplete. Cleanup/export
errors likewise report preservation rather than claiming a clean result.
Temporary storage has the operating system's usual retention limits; callers
requiring durable inspection should provide a session-data directory.

## Integration API

`internal/agent/worktree.go` exposes:

```go
NewWorktree(ctx context.Context, sourceRoot, parentDir string) (*AgentWorktree, error)
(*AgentWorktree).Finish(ctx context.Context) (WorktreeResult, error)
```

`AgentWorktree.SourceRoot` is the canonical Git top-level; `Path` is the child
checkout top-level and `Branch` is its unique branch. To retain a source cwd
below the repository root, resolve its relative path beneath `Path`. A non-nil
handle can accompany a creation error; never launch a child in that case.

Dispatch integration must construct independent child configuration, shell and
filesystem tool cwd, context-file discovery, and LSP clients rooted in that child
workspace. Do not mutate the parent configuration or share parent-rooted LSP
clients. Stop child operations and close its LSP clients before calling `Finish`.
Use a fresh bounded cleanup context after cancellation if inspection/export is
desired; passing the canceled run context preserves storage without inspection.
Always surface both the result and any error. Finish is serialized per handle,
and successful removal is idempotent.
