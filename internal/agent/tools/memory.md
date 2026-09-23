Memories that outlive the session; the index loads into every future session.

- `save` — create or update; needs title and content. Omitting id upserts by title; near-duplicate titles are flagged.
- `edit` — update by id or title (misspelled titles resolve when unambiguous); never creates.
- `read` — by id or query: one match returns the note in full, several return the ranked index.
- `search` — relevance-ranked over title and content (BM25, semantic, fuzzy); returns snippets. `list` — the index. `delete` — by id.
