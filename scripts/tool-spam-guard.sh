#!/usr/bin/env bash
# tool-spam-guard.sh — PreToolUse hook that makes runaway tool-call floods
# impossible. When the same tool is called more than MAX_CALLS times within
# WINDOW seconds in one session, further calls of that tool are denied until
# the window rolls over. The deny reason tells the agent to stop, reassess,
# and continue by hand or in a later turn.
set -euo pipefail

MAX_CALLS="${HARNESS_SPAM_GUARD_MAX:-60}"
WINDOW="${HARNESS_SPAM_GUARD_WINDOW:-60}"

tool="${HARNESS_TOOL_NAME:-unknown}"
session="${HARNESS_SESSION_ID:-default}"
dir="${TMPDIR:-/tmp}/harness-spam-guard/$session"
mkdir -p "$dir"
count_file="$dir/${tool//\//_}.count"
start_file="$dir/${tool//\//_}.start"

now=$(date +%s)

# Serialize concurrent hook invocations per session+tool; mkdir spin lock
# avoids needing flock and works everywhere.
lock="$dir/.lock-${tool//\//_}"
lock_tries=0
until mkdir "$lock" 2>/dev/null; do
	lock_tries=$((lock_tries + 1))
	if [ "$lock_tries" -ge 50 ]; then
		# Contention or a stale lock: allow rather than deadlock the agent.
		exit 0
	fi
	sleep 0.05
done
trap 'rmdir "$lock" 2>/dev/null || true' EXIT

start=$(cat "$start_file" 2>/dev/null || echo "$now")
count=$(cat "$count_file" 2>/dev/null || echo 0)
if [ $((now - start)) -ge "$WINDOW" ]; then
	start=$now
	count=0
fi
count=$((count + 1))
echo "$start" >"$start_file"
echo "$count" >"$count_file"

if [ "$count" -gt "$MAX_CALLS" ]; then
	remaining=$((WINDOW - (now - start)))
	[ "$remaining" -lt 1 ] && remaining=1
	echo "Rate limit: '$tool' has been called $count times in the last $((now - start))s (limit $MAX_CALLS per ${WINDOW}s). This looks like a runaway loop — stop repeating this tool call, summarize the current state, and ask the user how to proceed. Blocked for ${remaining}s more." >&2
	exit 2
fi

exit 0
