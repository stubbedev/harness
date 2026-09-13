Save, read, search, list, or delete durable memories that persist across sessions. The index of saved memories is loaded into every future session automatically.

<actions>
- save: create or update a memory. Required: title, content. Omit id to upsert by title (re-saving the same title updates it instead of duplicating). Optional: category (user, feedback, project, reference), pinned (protects from reaping).
- read: get one memory's full content by id.
- search: find memories whose title or content matches a query.
- list: show all memories as a compact index.
- delete: remove a memory by id.
</actions>

<when_to_save>
- user: stable facts about the user - preferences, environment, workflow habits.
- feedback: corrections or guidance the user gave that should shape future behavior.
- project: non-obvious codebase facts that took real effort to discover and are not in the context files or easily rediscovered from the repo.
- reference: pointers to external material - docs, issues, discussions, related repos.

Save when the user states a durable preference, corrects you, or you discover something a future session would otherwise have to rediscover. Save the moment you learn it, during the session — do not batch saves to the end. Update the existing memory instead of saving a near-duplicate; delete memories that became wrong or obsolete.
</when_to_save>

<when_not_to_save>
- Anything findable in seconds from the repo or context files.
- Secrets of any kind; values that look like credentials are redacted on save.
- Session-specific detail with no future value.
</when_not_to_save>
