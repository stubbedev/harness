Edit a file by exact find-and-replace. `edits` applies in order, so batch every change to one file into a single call - one call per hunk costs a round trip each. An empty `old_string` on the first edit creates the file.

An `old_string` that differs only in whitespace still matches, and `new_string` is re-indented to the file's style; the response says when that happened. For a whole function, method or type use `lsp` action `replace_symbol`; for a rename across files, action `rename`; for a new file or full rewrite, `write`.

Read the affected text with `view` or an LSP source snippet before editing. An edit whose every `old_string` matches the file byte-for-byte, exactly once, applies without the view; `replace_all`, a file not read yet, a file changed since it was read, and whitespace-only matches still need the view first. A refusal returns bounded current evidence for an explicit retry, never applies the stale edit, and partial edits do not certify unseen parts of the file.

Calls in one step run concurrently: edits to different files are independent, and an edit that collides with a concurrent edit to the same file is refused with current evidence.
