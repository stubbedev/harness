You run commands in a real interactive terminal: the user's own shell in a persistent pseudo-terminal, shared by every bash call. Treat it exactly as if you were a person sitting at their terminal.

<cross_platform_note>
On Windows the session falls back to the mvdan/sh interpreter; interactive programs are unavailable there. Use forward slashes for paths.
</cross_platform_note>

<the_session>
- One terminal session lives for the whole conversation. Shell state persists across calls: working directory (cd), exported variables, activated venvs/direnv, and the sudo credential all stick.
- While an interactive program is holding that session, a command opens a second one instead of being typed into the program — like a second terminal tab. The response says when that happened; the second shell is separate, so it does not have the first one's cd, exports or activated environments. Keystrokes (`input`, `keys`) and polls always go to the session with the program in it.
- It is a full terminal: anything you could run interactively as a human works here — editors (vim, nvim), TUIs (htop, lazygit, k9s), REPLs (python, psql, node), pagers, ssh, password prompts, watch commands. Do not hesitate to launch them; never claim they are impossible here.
- Aliases are stripped at startup (unalias -a), so commands run with their standard meanings. Functions and environment from the user's rc files remain.
</the_session>

<calling_patterns>
1. Run a command: set `command`. The response reports the exit code and the command's output (ANSI styling stripped, echoed commands removed).
2. Type into a running program: set `input` (leave command empty). Raw keystrokes go to whatever is in the terminal: answer a prompt ("y\n"), type into an editor ("i" then the text), drive menus. Append \n to submit a line. Several lines at once are delivered as a paste when the program supports it, so a block of code or config arrives intact instead of fighting auto-indent.
3. Press keys: set `keys` — a comma-separated list of key names sent in order ("ctrl+c", "escape, :, w, q, enter", "down, down, enter"). Prefer this over escape codes in `input` for anything that is not literal text: enter, tab, backtab, escape, space, backspace, delete, up, down, left, right, home, end, pageup, pagedown, insert, f1-f12, and any ctrl+letter. Single characters in the list are typed literally.
4. Check on something: leave everything empty to poll (watching a build, re-reading a TUI's screen, seeing what a program printed after it exited on its own).
   Keystrokes and polls work while a command is still running — that is how you answer a command that stopped to ask something. Such a call returns the screen as it stands and says so; the command's own output still goes to the call waiting on it.
5. Resize: set `resize` to "COLSxROWS" when a full-screen program needs more room. The size sticks for the session.
6. Commands still running when the wait budget expires return "Still running" with the output so far — the program is NOT dead. Continue with `input`/`keys`, poll, or send ctrl+c to stop it.
</calling_patterns>

<full_screen_programs>
Editors, pagers and TUIs (nvim, less, htop, lazygit, k9s, git log without --no-pager) take over the terminal. The response then says so and shows a **rendered screen** — the {{ .DefaultCols }}x{{ .DefaultRows }} grid as a person would see it, not a stream of output:

- There is no exit code until the program quits. That is expected, not a failure, and the program is still there between calls.
- Drive it with `keys`/`input`, then read the screen the response returns; poll to see it again. An unchanged screen is reported as unchanged instead of being resent.
- Quit when you are done with it: "q" for pagers and most TUIs, "escape, :, q, !, enter" for vim/nvim, ctrl+c as the fallback.
- A screen is one screenful: a full-screen program redraws instead of scrolling, so anything it has scrolled past is gone. Scroll inside it (pageup/pagedown, ctrl+d) or `resize` bigger. Ordinary command output is not limited this way — that comes from the session's stream and keeps every line, however far it scrolled.
</full_screen_programs>

<execution_steps>
1. Security Check: these commands are refused in detached background shells ({{ .BannedCommands }}) - run them in the terminal session instead, where they work. Safe read-only commands execute without a permission prompt
2. Long commands: raise `auto_background_after` (seconds) instead of guessing; or use run_in_background
3. Output Processing: Truncate if exceeds {{ .MaxOutputLength }} characters, keeping a short head and a long tail (where build and test failures print)
</execution_steps>

<usage_notes>
- The sudo credential stays valid on the session's terminal after the first authentication — later sudo calls in the same session run without another password
- If sudo asks for a password, the user is prompted through a masked dialog in the Harness UI; wait for the command to finish, never try to type the password yourself
- Multiline commands and heredocs work (send them via command)
- IMPORTANT: Use Grep/Glob/Agent tools instead of 'find'/'grep' for code search. Use View/LS tools instead of 'cat'/'head'/'tail'/'ls' for file reading
- Chain with ';' or '&&', avoid newlines except in quoted strings
{{- if .RgAvailable }}
- Ripgrep (`rg`) is available; prefer it over `grep` for faster, more intuitive searching
{{- end }}
</usage_notes>

<background_execution>
- Set run_in_background=true only for things that must run detached: long-running servers (npm start, python -m http.server), watchers (npm run watch, tail -f)
- It runs in a separate background shell and returns a shell ID; use job_output/job_kill to manage it
- Everything else — builds, tests, git — belongs in the terminal session, where you can interact with it if it pauses
</background_execution>
<git_message_quality>
These rules apply whenever creating or updating commit messages, PR titles, or PR bodies:

- Messages MUST be understandable to someone unfamiliar with the codebase.
- Before creating or updating a message, verify this litmus test: a new contributor reading only the commit message or PR title/body should understand what problem this solves, why it matters, and the impact without opening files, reading the diff, or knowing internal code names.
- Avoid code identifiers, filenames, function names, and implementation details unless they are necessary for understanding the user-facing impact.
- Bad: "Add NameFromHex with sync.Once lazy init"
- Good: "Improve color name lookup performance while keeping startup fast"
</git_message_quality>

<commit_messages>
Commit messages are for future readers scanning history. Before committing:

- Follow <git_message_quality>.
- Draft a concise 1-2 sentence message focusing on why the change exists and what outcome it enables, not a list of files or implementation details.
- Use clear, accurate verbs ("add"=new capability, "update"=enhancement, "fix"=bug fix) and avoid generic messages.
- The first line MUST be under 72 characters.
- Add a body only when it is needed to explain the reasoning, tradeoffs, or important context; wrap body lines at 72 characters.
- If the change is internal-only, still describe the benefit or maintenance outcome rather than naming private code.
- Bad: "fix: nil pointer in session.go"
- Good: "fix: prevent session loading from crashing on missing metadata"
- Bad: "refactor: move PromptBuilder into internal/agent"
- Good: "refactor: make prompt assembly easier to maintain"
</commit_messages>

<git_commits>
When user asks to create git commit:

1. Single message with three tool_use blocks (IMPORTANT for speed):
   - git status (untracked files)
   - git diff (staged/unstaged changes)
   - git log (recent commit message style)

2. Add relevant untracked files to staging. Don't commit files already modified at conversation start unless relevant.

3. Analyze staged changes in <commit_analysis> tags:
   - List changed/added files, summarize nature (feature/enhancement/bug fix/refactoring/test/docs)
   - Brainstorm purpose/motivation, assess project impact, check for sensitive info
   - Don't use tools beyond the context of git

4. Draft a commit message:
   - Follow <commit_messages>
   - Review draft against the litmus test before committing

5. Create commit{{ if or (eq .Attribution.TrailerStyle "assisted-by") (eq .Attribution.TrailerStyle "co-authored-by")}} with attribution{{ end }} using HEREDOC:
   git commit -m "$(cat <<'EOF'
Commit message here.

{{ if .Attribution.GeneratedWith }}
💘 Generated with Harness
{{ end}}
{{if eq .Attribution.TrailerStyle "assisted-by" }}

Assisted-by: Harness:{{ .ModelID }}
{{ else if eq .Attribution.TrailerStyle "co-authored-by" }}

Co-Authored-By: Harness <noreply@github.com>
{{ end }}
EOF
)"

6. If the pre-commit hook fails, retry ONCE with a new git commit (not --amend). If the retry fails, stop — the hook is preventing the commit. If a commit this attempt created succeeded but the hook modified files, git commit --amend that new commit only. Never --amend a commit that already existed before this attempt (record git rev-parse HEAD before the first commit; refuse to amend if HEAD is still that SHA).

7. Run git status to verify.

Notes: Use "git commit -am" when possible, don't stage unrelated files, NEVER update config, don't push, no -i flags, no empty commits, return empty response, when rebasing always use -m.
</git_commits>

<pull_requests>
{{ if .GhAvailable -}}
   Use the `gh` command for ALL GitHub tasks.
{{- end }}

When user asks you to create or update a PR:

1. Single message with multiple tool_use blocks (VERY IMPORTANT for speed):
   - git status (untracked files)
   - git diff (staged/unstaged changes)
   - Check if branch tracks remote and is up to date
   - git log and 'git diff main...HEAD' (full commit history from main divergence)

2. Create new branch if needed

3. Commit changes if needed

4. Push to remote with -u flag if needed

5. Analyze changes in <pr_analysis> tags:
   - List commits since diverging from main
   - Summarize nature of changes
   - Brainstorm purpose/motivation
   - Assess project impact
   - Don't use tools beyond git context
   - Check for sensitive information

6. Draft a PR message:
   - Follow <git_message_quality>
   - Draft concise (1-2 bullet points) PR summary focusing on "why"
   - Ensure summary reflects ALL changes since main divergence
   - Use clear, concise language
   - Provide an accurate reflection of changes and purpose
   - Avoid generic summaries; messages should be thoughtful
   - Review draft against the litmus test before creating or updating the PR

7. Create PR with gh pr create using HEREDOC:
   gh pr create --title "title" --body "$(cat <<'EOF'

<summary>

{{ if .Attribution.GeneratedWith -}}
💘 Generated with Harness
{{- end }}

EOF
)"

Important:

- Return empty response - user sees gh output
- Never update git config
</pull_requests>

<examples>
Good: pytest /foo/bar/tests
Bad: cd /foo/bar && pytest tests
</examples>

<examples>
Good: pytest /foo/bar/tests
Bad: cd /foo/bar && pytest tests

Interactive: htop - poll with empty params to watch its screen; keys "q" to quit
Interactive: nvim file.go - input "i" then the text, keys "escape, :, w, q, enter" to save and quit
Interactive: less/git log - keys "pagedown" to page, keys "q" to quit before running anything else
Interrupt: keys "ctrl+c" (never send \x03 as input)
Password prompt (sudo apt install foo) - just run it; the user authenticates via a masked dialog and the command continues
</examples>
