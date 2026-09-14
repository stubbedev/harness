Save, read, search, list, edit or delete memories that outlive the session. Their index loads into every future session, so a memory is worth its place only if a later session would otherwise rediscover it the hard way.

- `save` — create or update. Needs title and content; omitting id upserts by title. Optional category and `pinned` (protects it from reaping).
- `edit` — update by id or exact title without risking a duplicate. Needs content plus one of them; omitted fields keep what is stored.
- `read`, `delete` — by id. `search` — over title and content. `list` — the whole index, compact.

Categories: `user` for stable facts about them, `feedback` for corrections that should shape how you work, `project` for non-obvious codebase facts that cost real effort to find, `reference` for pointers to external material.

Save the moment you learn it, not in a batch at the end. Update the existing memory rather than saving a near-duplicate, and delete ones that turned out wrong. Do not save what the repo or the context files already answer, session-only detail, or anything secret — credential-shaped values are redacted on save anyway.
