Run several tool calls as one composed plan, passing each step's output into the next with jq, and return only the filtered result.

Reach for it when you would otherwise make the same call over every item of a list, or chain calls whose arguments come from the last one's output: a plan that reads forty records and reports the three that are wrong costs one entry in the conversation instead of forty-one. Not for a couple of unrelated calls, and not when you need to read each intermediate result yourself — you never see them.

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

Steps run in order, each binding its output to a jq variable named after its `id` — `$hits`, `$files`. A step's output is its tool's response, parsed as JSON when the tool answers in JSON and the raw string otherwise.

- `id` — unique, and a valid jq identifier.
- `tool` — any tool you can call directly, except `batch`.
- `input` / `input_jq` — literal JSON, or a jq expression producing it; `input_jq` shallow-merges over `input`.
- `for_each` — a jq expression of items to fan out over. Runs once per item with `$item` and `$index` bound, 8 at a time, and outputs the results in item order.
- `when` — a jq expression gating the step; falsy skips it and its output is `null`.
- `on_error` — `fail` (default) aborts the plan; `collect` records `{"error": "..."}` and continues, which is what you want inside a `for_each` where one bad item should not lose the other thirty-nine.
- `return` — a jq expression over those variables, and **the only thing that enters the conversation**, so filter, count or project here. It defaults to the last step's output, which is rarely right for a wide fan-out.

Every expression evaluates against a `null` input: everything reachable arrives through `$variables`, so `.` is never the previous step. The plan is checked before it runs anything, and a failure reports the shape of each step output so far.
