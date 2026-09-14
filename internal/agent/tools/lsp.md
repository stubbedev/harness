Ask the language server about code, by symbol name rather than by text. Set `action`:

- `definition` — where a symbol is defined. Language-aware, so unlike a text search it skips comments, strings and partial identifiers.
- `references` — everywhere it is used.
- `call_hierarchy` — who calls it (`direction: incoming`) or what it calls (`outgoing`). Use before a refactor to see the blast radius.
- `symbols` — the outline of one `file_path`: names, kinds and line ranges. Worth a call before editing an unfamiliar file.
- `diagnostics` — errors, warnings and hints for one `file_path`, or the whole project when omitted. Edits and reads already report what is new on their own; this is for the full current picture, repeats included.
- `rename` — a true semantic rename across every file, respecting scope, overloads and imports. Always prefer it to editing a name by hand.
- `replace_symbol` — replace, delete or insert around a whole function, method or type, with the server resolving its exact bounds. Prefer it to `edit` for whole-symbol changes: nothing has to match whitespace. `mode` is `replace` (default), `add_before`, `add_after` or `delete`.
- `restart` — restart one client by `name`, or all of them, when results have gone stale.
