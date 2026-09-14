Ask the user a structured question and wait for the answer. For a choice only they can make: an ambiguous request, a decision with real tradeoffs, a confirmation before something hard to undo. Not for anything the code, the docs or another tool can answer.

One question renders plain; several render as a tabbed form ending in a confirmation screen, so ask everything you need at once rather than in a chain of calls. Batches need `confirm_title` and `confirm_description`; write the description as though you already know what they will pick, so they are confirming a plan rather than a list.

Every question needs a `type`, a one-line `question`, and a `description` giving the context or tradeoff behind it. Limits on counts and lengths are enforced, and a violation comes back naming what to fix.

- `yes_no` — accept or reject one proposition ("Proceed with deletion?"). Never for A-vs-B: if both answers name a real option, that is `single_choice` with two choices.
- `single_choice` — pick one of `choices`, including binary ones ("TypeScript or Go?").
- `multi_choice` — pick any number of `choices`.
- `free_text` — an answer no list can hold.

Choice questions already offer a free-text fill-in, so never add an "Other" or "Custom" choice yourself.
