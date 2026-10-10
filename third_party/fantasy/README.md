# fantasy (vendored fork)

Vendored fork of [charm.land/fantasy](https://github.com/charmbracelet/fantasy)
at `v0.45.2`, wired in through a `replace` directive in the root `go.mod` so
the import path stays `charm.land/fantasy`. It is the provider abstraction
and agent loop behind `internal/agent`: every model request Harness makes,
and every tool call the model makes back, goes through it.

## Why

The agent step loop in `agent.go` decides when the model's tool calls run.
Upstream waits for the whole assistant message to finish streaming before
it starts any tool, and runs each sequential tool on its dispatcher
goroutine, so parallel tools emitted after a slow sequential one wait for
it. Upstream's `PrepareStep` also cannot change a step's provider options,
which Harness needs to run tool-result steps at a lower reasoning effort.
All of these are carried here until they can be proposed upstream.

## Local changes

All of them are in `agent.go`. Tests are in `agent_early_dispatch_test.go`,
`agent_repair_routing_test.go` and `agent_step_provider_options_test.go`.

- **Early dispatch.** `WithEarlyToolDispatch` (or
  `AgentStreamCall.EarlyToolDispatch`) takes a predicate. A tool call that
  arrives in the stream starts at once, before the message has finished,
  when the tool is `Parallel`, the call is not provider-executed, its
  arguments pass validation as they stand (no repair is attempted
  mid-stream), and the predicate accepts it. Without a predicate nothing
  starts early, which is upstream behaviour. Only execution moves:
  `OnToolCall` still fires after the stream ends, in emission order, and an
  early call's `OnToolResult` is held until every `OnToolCall` has run. If
  the stream ends on anything but a tool-calls finish, the early call is
  canceled and waited for, and its result is dropped, so the step and its
  callbacks look exactly as they do upstream for a call that was never
  dispatched (CHARM-2020). `Parallel` alone is not the gate because a
  consumer may mark mutating tools parallel (Harness does: edit, write and
  shell are), and such a tool must not run for a turn that is then cut off.
- **Parallelism.** The per-step parallel limit is 16 instead of 5, and
  sequential tools no longer run on the dispatcher goroutine. Each one waits
  for the sequential tool emitted before it, so they still run one at a
  time in order, but a parallel tool emitted after a slow sequential one
  starts straight away instead of waiting for it.

- **Repair routing.** Upstream's `Stream` repaired tool calls with the
  repair function from the raw `AgentStreamCall`, not the prepared call
  that has the agent's `WithRepairToolCall` merged in, so an agent-level
  repair never ran for a streamed step and the built-in JSON repair ran
  instead. It also passed the agent's system prompt to repair rather than
  the one `PrepareStep` gave the step. `Generate` had the mirror bug: it
  always used the agent-level repair and ignored `AgentCall.RepairToolCall`.
  Both paths now repair with the call's function, falling back to the
  agent's, and with the step's system prompt.

- **Per-step provider options.** `PrepareStepResult.ProviderOptions`, when
  non-nil, replaces the call's provider options for that step only, layered
  over the agent's `WithProviderOptions` the same way the call's are. Nil
  keeps the call's, which is upstream behaviour. Both `Generate` and
  `Stream` honour it. Harness uses it to send the steps that digest tool
  results at a lower reasoning effort than the step that answers the user.

Results still land in step content in call order, whatever order the tools
finish in.

Not vendored: `providertests/` (provider integration tests and their 4.7MB
of recorded cassettes), `.github/`, `.helix/`, `scripts/`, `Taskfile.yaml`,
`.goreleaser.yml`, `.editorconfig`, `.gitattributes`, `.gitignore`,
`crush.json`, `cspell.json`, `AGENTS.md` and the upstream `README.md`. The
package tests are kept and run from this directory with `go test ./...`
(`providers/kronk` needs `libffi` at test time).
