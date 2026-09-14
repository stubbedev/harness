Run commands in a persistent `{{ .Shell }}` PTY shared by the whole conversation: cwd, variables, activated environments and held credentials carry across calls. A result ends with `<cwd>…</cwd>` only when the shell moved; otherwise it is where it was.

`command`, `input`, `keys` and `reset` are mutually exclusive; setting two is rejected. `description` is optional and shown to the user.

- `command` — returns when the process exits, stops for input, takes over the screen, or goes idle. Tracked from the process, so never poll on a timer.
- `input` — raw text to the running program ("y\n"). Multiple lines arrive as a paste, surviving auto-indent.
- `keys` — named keys in order ("ctrl+c", "escape, :, w, q, enter").
- empty call — block until the running command's next event.
- `reset` — kill a wedged session and start a fresh one. Last resort.

A program that takes the screen returns a rendered {{ .DefaultCols }}x{{ .DefaultRows }} frame plus how to drive and quit it; a `command` sent while it holds the session opens a second session. Output over {{ .MaxOutputLength }} characters keeps a head and a long tail. Password prompts are answered by the user in a masked dialog — wait, never type one.
{{- if .ModernTools }}
Installed here and preferred over the POSIX default: {{ .ModernTools }}.
{{- end }}

<background_execution>
`run_in_background` only for detached work — servers, watchers, `tail -f`. Returns a shell ID for `job`. Network, package-manager and privileged commands are refused. Builds, tests and git stay in the session, where you can answer them if they pause.
</background_execution>
