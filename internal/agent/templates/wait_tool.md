Block until background sub-agents finish and return their results. Pass the handles from `agent` calls made with `background: true`.

Returns early when a background agent sends a message, or at `timeout_seconds`. Results are kept, so waiting again on a finished handle returns its result again.
