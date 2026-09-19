Ask the language server about code by symbol name rather than by text. Set `action`:

- `definition` / `references` — where a symbol is defined, and everywhere it is used.
- `call_hierarchy` — callers (`direction: incoming`) or callees (`outgoing`).
- `symbols` — outline of one `file_path`: names, kinds, line ranges.
- `diagnostics` — errors, warnings and hints for one `file_path`, or the whole project when omitted.
- `rename` — semantic rename across every file, respecting scope, overloads and imports. Always prefer it to editing a name by hand.
- `replace_symbol` — replace, delete or insert around a whole function, method or type, bounds resolved by the server, so nothing has to match whitespace. `mode`: `replace` (default), `add_before`, `add_after`, `delete`.
- `restart` — restart one client by `name`, or all, when results have gone stale.

Definition source snippets count as reads only for their displayed lines; outlines and location lists do not. `replace_symbol` requires the current affected lines to have been read and refuses stale content with a bounded excerpt for retry. Semantic `rename` retains its workspace-edit policy and does not certify unseen file contents.
