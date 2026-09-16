Ask the user something only they can decide - an ambiguous request, a real tradeoff, a confirmation before something hard to undo - and wait for the answer. Not for anything the code, the docs or another tool can answer.

Ask everything you need in one call (up to 5 questions); a batch needs `confirm_title` and `confirm_description`, written as though you already know what they will pick. Each question has a `type`, a one-line `question` (max 240 characters) and a required `description` (markdown, up to 600 characters) giving the tradeoff behind it.

- `yes_no` - accept or reject one proposition. Never for A-vs-B: that is `single_choice` with two choices.
- `single_choice` / `multi_choice` - pick one or several of up to 10 `choices`. Every choice question already offers a free-text fill-in, so never add an "Other" choice.
- `free_text` - an answer no list can hold.
