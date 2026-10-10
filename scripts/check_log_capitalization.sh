#!/usr/bin/env bash
set -euo pipefail

status=0
git grep --untracked -nE 'slog\.(Error|Info|Warn|Debug|Fatal|Print|Println|Printf)\("[a-z]' -- '*.go' || status=$?
case "$status" in
0)
  echo "❌ Log messages must start with a capital letter. Found lowercase logs above."
  exit 1
  ;;
1) ;;
*) exit "$status" ;;
esac
