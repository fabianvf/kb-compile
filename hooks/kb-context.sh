#!/bin/bash
# Put the knowledge base in front of the agent, because it will not go and get
# it.
#
# This exists because of a measurement, not a hunch. Two agents were given a
# real bug in a repository whose AGENTS.md tells them, in as many words, to look
# a file up in the reverse index before changing it. Both went straight to grep.
# Neither opened `docs/kb/` at all, and both produced fixes that violated an
# invariant the owning article states in its first two lines - one putting a
# network call inside the program whose whole purpose is to be instant, the
# other writing a second implementation of a rule that already had one.
#
# The KB was accurate, current, and cheap to query. It was never queried. A
# routing table nobody reads routes nobody, so the delivery has to be pushed
# rather than pulled.
#
# UserPromptSubmit specifically: plain stdout from this event IS added to the
# model's context, which is not true of PreToolUse. Editing is where the
# knowledge matters most, but PreToolUse cannot reliably deliver it, so the
# next best moment is every time the human says something.

set -euo pipefail

INPUT=$(cat)
cd "${CLAUDE_PROJECT_DIR:-.}" 2>/dev/null || exit 0

# No config means this repo does not use kb-compile. Not our business.
[ -f .kb/config.yaml ] || [ -f .kb/config.yml ] || [ -f .kb/config.json ] || exit 0
command -v kb >/dev/null 2>&1 || exit 0

prompt=""
session=""
if command -v jq >/dev/null 2>&1; then
  prompt=$(printf '%s' "$INPUT" | jq -r '.prompt // empty' 2>/dev/null || true)
  session=$(printf '%s' "$INPUT" | jq -r '.session_id // empty' 2>/dev/null || true)
fi
[ -n "$session" ] || session="pid$PPID"

# The digest is the routing table and it is the same every prompt, so it goes
# in once per session. Repeating it would spend its own token budget over and
# over to say nothing new, which is how a helpful hook becomes an ignored one.
marker="${TMPDIR:-/tmp}/kb-context-$session"
if [ ! -e "$marker" ]; then
  : > "$marker" 2>/dev/null || true
  if digest=$(kb read DIGEST 2>/dev/null) && [ -n "$digest" ]; then
    echo "The knowledge base for this repository. Read the owning article"
    echo "BEFORE changing a file it covers; it carries decisions and invariants"
    echo "that are not in the code. \`kb owners <file>\` for one file,"
    echo "\`kb read <article>\` for the whole thing."
    echo
    printf '%s\n' "$digest"
  fi
  # A fresh session on an existing task is the case the resume view exists for.
  # Once per session, for the same reason as the digest, and because it asks git
  # for the working tree, which this hook must not do on every message.
  if [ -s .kb/tasks/CURRENT ] && ctx=$(kb task context 2>/dev/null) && [ -n "$ctx" ]; then
    echo
    printf '%s\n' "$ctx"
  fi
elif [ -s .kb/tasks/CURRENT ]; then
  # One line and no git, on every later prompt. The session marker survives
  # compaction and the resume view does not, so this is how an agent that has just
  # lost its context finds out there is something to read.
  echo "Current task: $(head -n 1 .kb/tasks/CURRENT 2>/dev/null || true). \`kb task context\` resumes it."
fi

# Anything in the prompt that looks like a repo path gets its invariants
# attached. This is the targeted half: when the human names a file, the thing
# that stops the next edit breaking something is one command away, and the
# agent has demonstrably not been running it.
[ -n "$prompt" ] || exit 0
paths=$(printf '%s' "$prompt" | tr -s '[:space:]`",()[]' '\n' |
        grep -E '^[A-Za-z0-9_./-]+/[A-Za-z0-9_.-]+\.[A-Za-z0-9]+$' | sort -u | head -12 || true)
[ -n "$paths" ] || exit 0

tracked=$(printf '%s\n' $paths | while read -r p; do
  [ -n "$p" ] && git ls-files --error-unmatch "$p" >/dev/null 2>&1 && echo "$p"
done)
[ -n "$tracked" ] || exit 0

# shellcheck disable=SC2086
if out=$(kb owners $tracked 2>/dev/null) && [ -n "$out" ]; then
  echo
  echo "Files named in that message, and what is known about them:"
  printf '%s\n' "$out"
fi
