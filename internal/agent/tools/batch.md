Run several tool calls as one composed plan, passing each step's output into the next with jq, and return only the filtered result.

Use this when you would otherwise make the same call many times over a list, or chain calls where each one's arguments come from the last one's output. A plan that reads forty records and reports the three that are wrong costs one entry in the conversation instead of forty-one — the intermediate results stay inside the plan.

Do NOT use it for a couple of unrelated calls; issue those directly. It is also the wrong tool when you need to *read* every intermediate result yourself in order to decide what to do next, because you never see them.

## Shape

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

Steps run in order. Every step binds its output to a jq variable named after its `id`, so later steps and `return` reference it as `$hits`, `$files`, and so on.

## Fields

- `id` — required, unique, and a valid jq identifier (letters, digits, underscore, not starting with a digit).
- `tool` — the tool to call. Any tool you can call directly, except `batch` itself.
- `input` — literal JSON input.
- `input_jq` — a jq expression producing the input object; shallow-merges over `input`. This is how a step uses earlier results: `"{file_path: $item, offset: 0}"`.
- `for_each` — a jq expression producing the items to fan out over. The step then runs once per item with `$item` and `$index` bound, up to 8 at a time, and its output is the array of per-item results in item order. A single array value is iterated, so both `$hits` and `$hits | .[]` do what you would expect.
- `when` — a jq expression gating the step. Falsy (`false` or `null`) skips it, and its output is `null`.
- `on_error` — `fail` (default) aborts the whole plan on a tool error. `collect` records `{"error": "..."}` as that result and continues, which is usually what you want inside a `for_each` where one bad item should not lose the other thirty-nine.

## return

A jq expression over the step variables. **Its output is the only thing that enters the conversation**, so filter, count, or project here rather than handing back whole records. Defaults to the last step's output, which is rarely what you want for a wide fan-out.

Expressions evaluate against a `null` input — everything reachable arrives through `$variables`, so `.` is never the previous step.

## Reading tool output

A step's output is the tool's response parsed as JSON when the tool answers in JSON, and the plain response string otherwise. When you are unsure of a tool's shape, run one call directly first and look at it, then write the plan against what you saw.

## Limits

At most 32 steps, 256 items per `for_each`, and a 64KB return value. Hitting the return cap means the `return` expression is too broad; narrow it rather than splitting the plan.
