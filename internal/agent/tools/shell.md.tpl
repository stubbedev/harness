A persistent `{{ .Shell }}` terminal shared by the whole conversation, used the way a person uses one. `command` is what you type: at the prompt it is a command line and runs (Enter implied); while a program is running it goes to that program - an answer, keystrokes, a block of lines - so write `\n` or `<enter>` where a person would press it. Named keys go in angle brackets: `<escape>`, `<tab>`, `<backspace>`, `<delete>`, `<up>`/`<down>`/`<left>`/`<right>`, `<home>`, `<end>`, `<pageup>`, `<pagedown>`, `<f1>`–`<f12>`, `<ctrl+c>` or any `<ctrl+letter>`; anything else in brackets is typed literally.

A call returns when the process exits (with its exit code), stops for input, takes over the screen (a rendered {{ .DefaultCols }}x{{ .DefaultRows }} frame instead of a stream), or goes idle. An empty call waits for the next of those. `reset` kills a wedged session and opens a fresh one, losing its state; last resort.

cwd, variables, activated environments and held credentials carry across calls; a result ends with `<cwd>…</cwd>` only when the shell moved. Output over {{ .MaxOutputLength }} characters keeps a head and a long tail.
{{- if .ModernTools }}
Installed here and preferred over the POSIX default: {{ .ModernTools }}.
{{- end }}

<background_execution>
`run_in_background` only for detached work — servers, watchers, `tail -f`. Returns a shell ID for `job`. Network, package-manager and privileged commands are refused. Builds, tests and git stay in the session, where you can answer them if they pause.
</background_execution>
