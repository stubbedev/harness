Dispatch a piece of work to a sub-agent. Several `agent` calls in one message run concurrently.

- `fast` — shell, view, edit, write on the small model. The default when `subagent_type` is omitted.
- `task` — same tools, large model, for the open-ended piece that needs judgment.
- `subagent_type` also lists this project's specialized agents and the model each runs on.

A sub-agent sees none of this conversation and returns only its final message. State the whole question, the paths to start from, and the answer shape you want. It cannot dispatch further sub-agents. Concurrency is capped; a wider fan-out runs in waves.

A dispatch returns a handle immediately. Messages it sends with `send_message` arrive between your steps. Collect results by calling `agent` again with no `prompt`: it waits for the `handles` given (all of them when omitted), returns early when one sends a message, and works across turns; a finished handle returns its kept result. `blocking: true` waits in this call instead.
