#!/bin/bash
# Block `git commit` while the KB is stale for the source being committed.
#
# The gate this replaces asked a question it could not answer: "were any files
# under docs/kb/ staged?" Staging an unrelated typo fix in any article
# satisfied that, and changing a subsystem without touching its article did
# not trip it. It measured activity, not agreement.
#
# `kb fresh` asks the real question — does any KB-owned file differ from the
# content its article was last checked against — and names the articles to
# re-read. This hook just surfaces that answer at the moment it matters.
#
# It never runs `kb compiled`. A hook that records the judgement makes the
# check pass always and assert nothing.

set -euo pipefail

INPUT=$(cat)
COMMAND=$(printf '%s' "$INPUT" | jq -r '.tool_input.command // empty' 2>/dev/null || echo "")

# Only act on commits.
printf '%s' "$COMMAND" | grep -q 'git commit' || exit 0

cd "${CLAUDE_PROJECT_DIR:-.}"

# No config means this repo does not use kb-compile. Not our business.
[ -f .kb/config.json ] || exit 0
command -v kb >/dev/null 2>&1 || exit 0

if OUT=$(kb fresh 2>&1); then
  exit 0
fi

REASON=$(printf '%s\n\n%s' \
  "The knowledge base is stale for source in this commit." "$OUT")

jq -Rn --arg r "$(cat <<< "$REASON")" '{
  hookSpecificOutput: {
    hookEventName: "PreToolUse",
    permissionDecision: "deny",
    permissionDecisionReason: $r
  }
}'
exit 0
