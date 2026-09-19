Edit a file by exact find-and-replace. `edits` applies in order, so several changes to one file belong in one call. An empty `old_string` on the first edit creates the file.

An `old_string` that differs only in whitespace still matches, and `new_string` is re-indented to the file's style; the response says when that happened. For a whole function, method or type use `lsp` action `replace_symbol`; for a rename across files, action `rename`; for a new file or full rewrite, `write`.

Read the affected text with `view` or an LSP source snippet before editing. Changed file contents are refused even if the timestamp is unchanged. A refusal returns bounded current evidence for an explicit retry; it never applies the stale edit. Partial edits do not certify unseen parts of the file. Shell output does not certify reads.
