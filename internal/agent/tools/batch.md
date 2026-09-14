Run several tool calls as one plan, piping each step's output into the next with jq, and return only the filtered result. For fanning one call out over a list, or chaining calls whose arguments come from the previous output: forty reads reporting three bad records cost one conversation entry instead of forty-one. Not for a couple of unrelated calls, and not when you need to read the intermediate results — you never see them.

```json
{
  "steps": [
    {"id": "hits", "tool": "shell", "input": {"command": "rg --json TODO internal"}},
    {"id": "files", "tool": "view", "for_each": "$hits.matches | map(.path) | unique",
     "input_jq": "{file_path: $item}"}
  ],
  "return": "$files | map(select(.content | test(\"FIXME\"))) | length"
}
```

Each step binds its output to `$<id>`, parsed as JSON when the tool answers in JSON, else the raw string.

- `id` — unique, valid jq identifier. `tool` — any tool but `batch`.
- `input` / `input_jq` — literal JSON, or jq producing it; `input_jq` shallow-merges over `input`.
- `for_each` — jq listing items to fan out over. Runs once per item with `$item`/`$index`, 8 at a time, output in item order.
- `when` — jq gate; falsy skips the step, output `null`.
- `on_error` — `fail` (default) aborts; `collect` records `{"error": "..."}` and continues, which is what a `for_each` wants.
- `return` — jq over those variables, and **the only thing that enters the conversation**. Defaults to the last step's output, rarely right for a fan-out.

Every expression evaluates against `null` input: everything arrives through `$variables`, so `.` is never the previous step. The plan is validated before anything runs.
