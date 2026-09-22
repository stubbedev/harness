---
name: jq
description: Use when invoking jq in the harness shell — the command is a built-in gojq, not the standard jq binary, and differs in supported flags and behavior.
---

# jq — Built-in, Not the Standard Binary

The shell tool's `jq` is Harness's built-in command (via
`github.com/itchyny/gojq`). No external jq binary is involved — never install
one, and expect the differences below rather than the man page.

## Differences from standard jq

- Object keys are sorted by default; `keys_unsorted` and `-S` do not exist.
- Integers are arbitrary precision — large ones keep full precision in
  arithmetic.
- String indexing works: `"abcde"[2]` returns `"c"`.

Unsupported: `--ascii-output`, `--seq`, `--stream`, `--stream-errors`,
`-f`/`--from-file`, `--slurpfile`, `--rawfile`, `--args`, `--jsonargs`,
`input_line_number`, `$__loc__`, regex backreferences and look-around.

Supported beyond the basics (`-r` `-j` `-c` `-s` `-n` `-e` `-R`):
`--arg name value`, `--argjson name value`, and file arguments after the
filter (`jq '.foo' file.json`).

gojq's `--yaml-input`/`--yaml-output` are not exposed here; use `yq` for
YAML.
