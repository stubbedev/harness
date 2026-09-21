Save, read, search, list, edit or delete memories that outlive the session. The index loads into every future session, so save only what a later session would otherwise rediscover the hard way.

- `save` — create or update; needs title and content. Omitting id upserts by title. Optional category and `pinned` (protects from reaping).
- `edit` — update by id or exact title; omitted fields keep what is stored.
- `read` — by id or query: one match returns the note in full, several return the index. `delete` — by id. `search` — title and content. `list` — the index.

Categories: `user` (stable facts about them), `feedback` (corrections that shape how you work), `project` (non-obvious codebase facts that cost real effort to find), `reference` (pointers to external material).

Save the moment you learn it. Update rather than saving a near-duplicate, and delete what turned out wrong. Not what the repo or context files already answer, and not session-only detail.
