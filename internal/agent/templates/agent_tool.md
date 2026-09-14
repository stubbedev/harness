Dispatch work to sub-agents. Several `agent` calls in one message run concurrently — that is the point. Split a task into independent pieces (per file, package, symbol, hypothesis) and dispatch one call each.

- `fast` — shell, view, edit, write on the small model. The default when `subagent_type` is omitted. Cheap: prefer several `fast` calls over one `task`, and over reading the files yourself.
- `task` — same tools, large model. Only for the open-ended piece that needs judgment.
- `subagent_type` also lists this project's specialized agents and the model each runs on. Prefer one whose description matches; a `cheap` one fans out like `fast`.

A sub-agent sees none of this conversation and returns only its final message — its tool output never reaches you. State the whole question, the paths to start from, and the answer shape you want. It cannot dispatch further sub-agents. Concurrency is capped; a wider fan-out runs in waves.

`background: true` returns a handle immediately instead of the result. Messages the agent sends with `send_message` arrive between your steps; `wait` on the handles collects the results, repeatedly if needed. A background agent outlives your turn. Use the blocking form when you cannot proceed without the result.
