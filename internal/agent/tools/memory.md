Memories that outlive the session; the index loads into every future session.

- `save` — create or update; needs title and content. Omitting id upserts by title.
- `edit` — update by id or exact title; omitted fields keep what is stored.
- `read` — by id or query: one match returns the note in full, several return the index.
- `search` — relevance-ranked over title and content (BM25, semantic, fuzzy); returns snippets. `list` — the index. `delete` — by id.
