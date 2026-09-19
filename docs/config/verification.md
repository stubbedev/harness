# Configured verification

Verification runs only commands explicitly listed in trusted configuration. Harness
never guesses a test command from repository files or sends an argv through a shell.
Review project configuration before enabling it: a configured executable has your
user's privileges, just like a terminal command. An explicitly configured shell
executable is still a shell; argv execution is not a sandbox.

```yaml
verification:
  require_on_completion: true
  max_repair_attempts: 2
  rules:
    - name: verification-unit
      paths:
        - internal/verification/**/*.go
        - go.mod
        - go.sum
      inputs:
        - internal/procgroup/**/*.go
      command: [go, test, ./internal/verification]
      timeout_seconds: 60
      max_output_bytes: 32768
```

`paths` are workspace-relative, slash-separated doublestar globs. A rule runs when
any path in the session's file history or Git's staged, unstaged, or untracked set matches; deleted files
and paths not currently present still select checks. Absolute changed paths inside
the workspace are normalized. Paths outside it are rejected. Multiple matches run
the rule once, in configuration order. `inputs` adds fingerprint dependencies but
does not select the rule; put shared files in `paths` too if changing them should
select the check. Include every local input the command depends on.

`command` is a nonempty executable-and-arguments array. Arguments are literal:
variables, substitution, wildcards and shell operators are not expanded. Commands
inherit the process environment and run at the workspace root. The model cannot
supply a command, override arguments, or select changed paths through `verify`.
Executable resolution uses the normal process PATH; configuration is trusted, not
an executable allowlist sandbox.

Timeout defaults to 120 seconds (maximum 3600). Combined stdout/stderr defaults to
32768 bytes (maximum 1048576); excess output is discarded, with `truncated: true`.
Zero selects these defaults; negative bounds are invalid. Cancellation terminates
the process tree through the existing platform process-group/job implementation.
Windows job-assignment failure blocks the check. This inherits the existing
process-tree implementation's limitations for deliberately detached processes.

## Results and revisions

The `verify` tool accepts `{}`. It returns a structured JSON result:

- `passed`: all applicable configured commands exited zero.
- `failed`: at least one command exited nonzero.
- `skipped`: no configured rule applies; this is not evidence that tests passed.
- `blocked`: missing executable, cancellation/timeout, unavailable fingerprint,
  unavailable ledger, or relevant content changed while checks ran.

Checks include their configured name/argv, status, exit code, duration, bounded
output, timeout flag and truncation flag. The aggregate includes normalized changed
paths, start/end timestamps, `revision_before` and `revision_after`. Only matching
rules execute; unrelated rules are not reported as passing.

Revisions hash the configuration, workspace identity, ledger paths, and the names,
modes and content of all files matching applicable `paths` and `inputs`, including
dirty, untracked and ignored files. They are content revisions, not Git HEAD IDs.
Deletions and new matching files change the fingerprint; unrelated content does
not. Git administrative data is excluded. Relevant symlinks and special files are
blocked rather than followed; directory symlinks block fingerprinting
conservatively. Fingerprinting works without Git and with unborn repositories.

Pre/post fingerprints must agree. A mismatch blocks the aggregate even if each
command exited zero; the checks describe what actually ran, not valid evidence for
the new revision. `Current` recomputes the fingerprint before evidence is reused.
These are boundary snapshots, not a filesystem transaction: changes reverted
between snapshots, external dependencies, environment changes and remote services
cannot be proven stable by a workspace content hash. Commands should not mutate
inputs or leave background work running.

## Integration contract

The verification package does not own the execution ledger or conversation loop.

1. Build `verification.New(workspaceRoot, cfg.Verification)`. Invalid configurations
   fail construction and normal config loading. Rebuild after configuration reload.
2. Register `agent.NewVerificationTool(runner, changedPaths, retain)` in the normal
   tool allowlist **before** the existing `wrapToolsWithHooks` boundary. Preserve
   tool enable/disable policy. `PreToolUse` sees the `verify` call and may deny it;
   do not call `Runner.Run` from the completion gate or bypass the tool wrapper.
3. `changedPaths(context.Context)` returns the parent-owned ledger's current paths,
   including tracked deletions and relevant terminal mutations. Return an error
   rather than silently supplying an empty list when attribution is unavailable.
4. `retain(context.Context, verification.Result)` retains the result per session.
   The tool also returns `Metadata()` as `{"verification": ...}` in standard tool
   response metadata, so existing message persistence retains structured evidence
   without a new database table. Retain failures become blocked tool responses.
5. Before claiming completion call `runner.Gate(ctx, changedPaths, latestResult,
   repairAttempts)`. With `require_on_completion: false` (default), completion is
   unchanged. With it enabled, no applicable rules need no run; otherwise require
   current passing evidence. The returned action is `complete`, `verify`, `repair`
   or `blocked`. `Allow` is true only for `complete`.
6. At normal top-level completion, Harness runs missing or stale checks through
   the registered, hook-wrapped `verify` tool without another model request.
   Failed checks can resume the model for repair through ordinary tools, up to
   `max_repair_attempts` (default zero, maximum ten). Blocked checks, hook denial,
   unavailable tools, or exhausted repairs stop completion with an error. The
   gate rechecks content fingerprints before accepting the result. Subagents do
   not run an independent completion gate; their file history is included in the
   parent's verification.

A hook denial occurs outside the inner tool, so the parent should preserve that
blocked tool outcome rather than treat an older retained result as a new run.
If the tool is disabled, the completion gate must not run commands behind that
policy boundary. Persisted JSON is evidence produced by execution, not an input
that the model is allowed to forge into successful verification.
