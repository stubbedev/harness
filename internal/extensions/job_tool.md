Collect the results of background jobs started by Lua extensions.

An extension tool that kicks off slow work returns a job ID instead of a result. The work keeps running after that tool call ends, and its result waits in a queue until you collect it here.

- `list` shows every job, its state (running, done, failed, canceled) and how long it has been at it.
- `result` without an ID hands back every finished result nobody has collected yet, and marks them collected. With an `id` it returns that job's result, optionally waiting up to `wait` seconds for a job still running.
- `cancel` stops a running job.

Results are handed out once. Collect them when the work they were started for matters; a long job is usually worth checking on after you have done something else.
