Wait for background agents started with the `agent` tool (`background: true`) and collect what they produced.

Returns as soon as one of these happens:
- every listed handle has finished — the response carries each agent's final result;
- a message arrives from one of the listed agents — the response carries the message plus current statuses, and you can go back to work or wait again;
- the timeout expires — the response carries the current statuses so you can decide whether to keep waiting.

Parameters:
- `handles`: the handles returned by the background `agent` calls.
- `timeout_seconds`: default 600, maximum 3600. Pass 0 for a non-blocking status check.

Notes:
- Results stay available: waiting again on a finished handle returns its result again.
- Messages are delivered exactly once — either here, or as the new user messages that appear between your steps while background agents run, never both.
- An agent that failed or was cancelled reports its status with the error in place of a result.
