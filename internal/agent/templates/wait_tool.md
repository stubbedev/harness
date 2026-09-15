Block until background sub-agents finish and return their results. Pass the handles returned by `agent` calls.

Returns early when a background agent sends a message, or at `timeout_seconds`. Results are kept, so waiting again on a finished handle returns its result again.
