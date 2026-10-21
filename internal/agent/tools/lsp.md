Ask the language server about code by symbol name rather than by text. Set `action`:

- `definition` / `references` — where a symbol is defined, and everywhere it is used. These, `call_hierarchy` and `rename` search under `path` (the working directory when omitted).
- `call_hierarchy` — callers (`direction: incoming`) or callees (`outgoing`).
- `symbols` — outline of the file at `path`: names, kinds, line ranges.
- `diagnostics` — errors, warnings and hints the servers hold now, for the file at `path`, or the whole project when `path` is omitted. It does not wait for analysis in progress; whatever the servers report later is delivered to you before your next step, so never poll it.
- `rename` — semantic rename across every file, respecting scope, overloads and imports. Always prefer it to editing a name by hand.
- `replace_symbol` — replace, delete or insert around a whole function, method or type in the file at `path`, bounds resolved by the server, so nothing has to match whitespace. `mode`: `replace` (default), `add_before`, `add_after`, `delete`.
- `restart` — restart one client by `name`, or all, when results have gone stale.

Definition source snippets count as reads only for their displayed lines; outlines and location lists do not. `replace_symbol` requires the current affected lines to have been read and refuses stale content with a bounded excerpt for retry. Semantic `rename` retains its workspace-edit policy and does not certify unseen file contents.
