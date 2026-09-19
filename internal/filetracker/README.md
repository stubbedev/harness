# File evidence

The production tracker records SHA-256 content versions and observed byte ranges, scoped to a session and absolute file path. The existing `Service` interface remains compatible with mocks; the optional `Evidence` interface supplies strict checks. Legacy implementations retain timestamp checks. `RecordRead` alone updates UI/history timestamps, not content evidence.

`view` records ranges from the same byte snapshot used to render its response, only once that response is returned. Text line-ending delimiters are included; truncated line tails are excluded. Single-image responses record the transmitted bytes, while images rejected by multi-view do not. Directory listings are sorted, nonrecursive, capped at 200 entries and paginated by entry offset; names do not certify file contents. LSP definition snippets register exactly their displayed source lines; symbol outlines, references, call hierarchy and diagnostics contain locations rather than source reads.

`edit` checks the current version and affected ranges. `replace_all` requires every affected occurrence to be observed. Successful edits carry forward previously observed unchanged bytes and tool-created replacement bytes, never unseen unchanged contents. `write` requires full coverage of an existing file. Tool-created files and full rewrites provide full coverage of the resulting content.

A stale edit or write is refused, never automatically rebased or applied. The response includes at most 2048 bytes of current UTF-8 evidence near the affected position, which is registered for an explicit retry. A missing old string falls back to the beginning of the file. Non-UTF-8 conflict content is not exposed or certified. Large files still require additional reads before a full rewrite. All changes anywhere in a file invalidate evidence for its previous version, even if a particular observed range is unchanged.

## Scope and limits

- Evidence is in memory for the service lifetime. Restarting requires new reads; persisted timestamps do not authorize edits.
- Shell output, arbitrary MCP/extension output, UI reads, prompt/context injection, embedded skill resources, and generated descriptions cannot certify filesystem reads. Use `view` or LSP definition source snippets.
- LSP `replace_symbol` checks the affected symbol lines and uses the guarded writer. Semantic multi-file `rename` retains its existing workspace-edit policy; it does not certify whole-file reads and is not a transactional guarded edit.
- Guarded edit/write/replace-symbol calls serialize by canonical path within this process. Creation uses exclusive creation; replacement writes a temporary sibling and compares current bytes immediately before atomic rename. This prevents known stale writes and cooperating-tool races, but the filesystem offers no portable compare-and-swap: an uncooperative external writer can still race between the final comparison and rename. Multi-file atomicity, hard-link identity, ownership/extended-attribute preservation and cross-process locking are not provided. Replacement preserves permission bits and resolves symlinks.
- Text views snapshot the entire file in memory to hash the same bytes they render. The output limit bounds returned text, not input memory. Directory sorting similarly reads the directory entries before capping the response.
