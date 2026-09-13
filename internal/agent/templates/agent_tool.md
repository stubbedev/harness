Dispatch work to one or more sub-agents. Sub-agents run concurrently: issuing several `agent` calls in a single assistant message runs them all at once, and that is the point of this tool. Whenever a task splits into independent pieces — one per file, per package, per symbol, per hypothesis — dispatch one call per piece in one message instead of working through them in sequence yourself.

Built-in types:
- `fast`: read-only (glob, grep, ls, view) on the small model — the default when `subagent_type` is omitted. For any well-scoped lookup or survey piece. It is cheap, so prefer splitting a survey into several `fast` calls over one `task` call — and over reading every file yourself.
- `task`: the same read-only tools on the large model. An escalation for the genuinely open-ended piece that needs judgment; do not reach for it when a cheap, well-scoped pass would answer the question.

The `subagent_type` parameter also lists the project's specialized agents with the model each runs on. Prefer a specialized agent whose description matches the task over the generic types. A specialized agent marked cheap is, like `fast`, worth fanning out rather than calling once.

Each sub-agent starts with no knowledge of this conversation and returns only its final message — you never see its tool output. So give every dispatch a self-contained prompt that states the full question, the paths or symbols it should start from, and the exact shape of the answer you want back. A prompt that assumes shared context comes back useless.

Sub-agents cannot dispatch further sub-agents. Both built-in types are read-only; a specialized agent can change files only when its own definition grants it write tools, so assume read-only unless its description says otherwise. Concurrency is capped, so a fan-out wider than the limit runs in waves rather than failing.
