Wait for background work to finish. One call covers everything this session has running: shell commands started with `run_in_background` (`job-N`), armed timers (`timer-N`) and background `agent` dispatches (`bg-…`).

`wait_for` names the ids to wait on, of any kind. Leave it empty to wait on everything still outstanding. The call returns as soon as any one of them finishes, with its result and the state of the rest, so nothing here needs polling or a sleep: start the work, do what else you can, then wait. Each result is returned by exactly one wait, and a result an earlier wait already returned is not repeated.

`timeout_seconds` bounds the wait in seconds: default 600, maximum 3600. 0 never blocks; it returns the current snapshot of everything outstanding immediately.

The wait also returns early when a background agent sends you a message, when the user queues a new prompt, or when the turn is cancelled; the response then says what is still running. Work keeps running after the wait returns - a job or timer is stopped only with its session - so a wait that times out costs nothing but the wait.
