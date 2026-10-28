Dispatch a piece of work to a sub-agent. Several `agent` calls in one message run concurrently.

- `fast` — shell, view, edit, write on the small model. The default when `subagent_type` is omitted.
- `task` — same tools, large model, for the open-ended piece that needs judgment.
- `subagent_type` also lists this project's specialized agents and the model each runs on.

A sub-agent sees none of this conversation and returns only its final message. State the whole question, the paths to start from, and the answer shape you want. It cannot dispatch further sub-agents. Concurrency is capped: a dispatch beyond the cap still returns its handle at once and starts when a running agent finishes.

A dispatch returns a handle immediately. Messages it sends with `send_message` arrive between your steps. Collect its result with the `wait` tool: it waits until that agent - or any other background work you name - finishes, and returns the result with the state of the rest, so you can dispatch the next piece of work before waiting again. It works across turns, each result is returned once, and a wait can name a job or a timer alongside an agent handle. `blocking: true` waits in this call instead.

To change a running agent's instructions, call `agent` with `message` (and `handles`, or none for every running agent): it reads the text before its next step. `cancel: true` with `handles` stops agents when redirecting is not enough.
