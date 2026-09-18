Dispatch work to sub-agents. This is the exception, not the default: dispatch only when the work splits into several independent pieces, each substantial enough to justify its own dispatch — a sweep across files or symbols, unrelated checks that can run while you keep working. Anything one or two direct tool calls answer, you answer directly. Several `agent` calls in one message run concurrently.

- `fast` — shell, view, edit, write on the small model. The default when `subagent_type` is omitted. For substantial independent pieces, not single lookups.
- `task` — same tools, large model. Only for the open-ended piece that needs judgment.
- `subagent_type` also lists this project's specialized agents and the model each runs on. Prefer one whose description matches; the model is cost information, not a nudge to dispatch.

A sub-agent sees none of this conversation and returns only its final message — its tool output never reaches you. State the whole question, the paths to start from, and the answer shape you want. It cannot dispatch further sub-agents. Concurrency is capped; a wider fan-out runs in waves.

A dispatch returns a handle immediately; the sub-agent runs while you keep working. Messages it sends with `send_message` arrive between your steps. Collect results by calling `agent` again with no `prompt`: it waits for the `handles` given (all of them when omitted), returns early when one sends a message, and works across turns; waiting on a finished handle returns its kept result. `blocking: true` waits in this call instead; use it only when no further step is possible without the result.
