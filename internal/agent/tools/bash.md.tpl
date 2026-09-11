You run commands in a real interactive terminal: the user's own shell in a persistent pseudo-terminal, shared by every bash call. Treat it exactly as if you were a person sitting at their terminal.

<cross_platform_note>
On Windows the session falls back to the mvdan/sh interpreter; interactive programs are unavailable there. Use forward slashes for paths.
</cross_platform_note>

<the_session>
- One terminal session lives for the whole conversation. Shell state persists across calls: working directory (cd), exported variables, activated venvs/direnv, and the sudo credential all stick.
- It is a full terminal: anything you could run interactively as a human works here — editors (vim, nvim), TUIs (htop, lazygit, k9s), REPLs (python, psql, node), pagers, ssh, password prompts, watch commands. Do not hesitate to launch them; never claim they are impossible here.
- Aliases are stripped at startup (unalias -a), so commands run with their standard meanings. Functions and environment from the user's rc files remain.
</the_session>

<calling_patterns>
1. Run a command: set `command`. The response reports the exit code and the command's output (ANSI styling stripped, echoed commands removed).
2. Interact with a running program: set `input` (leave command empty). It sends raw keystrokes to whatever is in the terminal: answer a prompt ("y\n"), type into an editor ("i" to enter insert mode, "\x1b" then ":wq\n" to save and quit in vim), drive menus. Append \n to submit a line.
3. Check on something: leave both empty to poll for new output (watching a build, or seeing what a program printed after it exited on its own).
4. Commands still running when the wait budget expires return "Still running" with the output so far — the program is NOT dead. Continue with `input`, poll, or send ctrl-c ("\x03") to stop it.
</calling_patterns>

<execution_steps>
1. Security Check: Banned commands ({{ .BannedCommands }}) return error - explain to user. Safe read-only commands execute without prompts
2. Long commands: raise `auto_background_after` (seconds) instead of guessing; or use run_in_background
3. Output Processing: Truncate if exceeds {{ .MaxOutputLength }} characters
</execution_steps>

<usage_notes>
- The sudo credential stays valid on the session's terminal after the first authentication — later sudo calls in the same session run without another password
- If sudo asks for a password, the user is prompted through a masked dialog in the Crush UI; wait for the command to finish, never try to type the password yourself
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
💘 Generated with Crush
{{ end}}
{{if eq .Attribution.TrailerStyle "assisted-by" }}

Assisted-by: Crush:{{ .ModelID }}
{{ else if eq .Attribution.TrailerStyle "co-authored-by" }}

Co-Authored-By: Crush <crush@charm.land>
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
💘 Generated with Crush
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

Interactive: htop - poll with empty params to watch; send "q" via input to quit
Interactive: nvim file.go - input "i" to type, escape then ":wq" + newline to save and quit
Password prompt (sudo apt install foo) - just run it; the user authenticates via a masked dialog and the command continues
</examples>
