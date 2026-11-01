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
#
# ORDER MATTERS HERE. Every check below runs AFTER the config test, so that a
# repo which does not use kb-compile is never denied anything, and a repo
# which does is never waved through. The previous order tested for the tools
# after using them, so a machine without `jq` took the "not a commit" exit and
# reported success — the same silent no-op the config-path bug produced, in a
# second spelling.

set -euo pipefail

INPUT=$(cat)

cd "${CLAUDE_PROJECT_DIR:-.}"

# No config means this repo does not use kb-compile. Not our business.
#
# All three of config.SearchPaths, not just one: the default is
# `.kb/config.yaml` and that is what kb-init writes, so testing only for
# `.kb/config.json` made this hook exit 0 without ever running `kb fresh` —
# a gate that silently stops gating, in a repo that ships it as an example.
[ -f .kb/config.yaml ] || [ -f .kb/config.yml ] || [ -f .kb/config.json ] || exit 0

# Past this line the repo demonstrably uses kb-compile, so a missing tool is a
# broken installation rather than a reason to stand down.

deny() {
  # Built with jq when jq is what we have, by hand when it is what we lack.
  if command -v jq >/dev/null 2>&1; then
    jq -Rn --arg r "$1" '{
      hookSpecificOutput: {
        hookEventName: "PreToolUse",
        permissionDecision: "deny",
        permissionDecisionReason: $r
      }
    }'
  else
    # The only caller in this branch passes a fixed single-line ASCII string
    # with no quote, backslash or newline, so this needs no escaping.
    printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"%s"}}\n' "$1"
  fi
  exit 0
}

if ! command -v jq >/dev/null 2>&1; then
  # jq reads the command out of the payload, so without it this hook cannot
  # tell a commit from any other Bash call. Matching the raw payload is a
  # cruder test, and it is only used to decide whether to report the broken
  # install — never to let a commit through.
  [[ $INPUT == *"git commit"* ]] || exit 0
  deny "This repo uses kb-compile, but jq is not on PATH and the commit gate needs it to read the tool payload. Install jq, or remove the hook. A gate that cannot run is not a commit that is safe to make."
fi

COMMAND=$(printf '%s' "$INPUT" | jq -r '.tool_input.command // empty')

# Only act on commits. A bash pattern rather than an external matcher: this
# runs on whatever PATH the agent happens to have, and a hook that needs a
# second outside tool to decide whether to run has a second way to fail open.
[[ $COMMAND == *"git commit"* ]] || exit 0

if ! command -v kb >/dev/null 2>&1; then
  # `kb install` records the build that wrote this hook, because whoever hits
  # this got the hook from a clone and has never heard of the project.
  version=$(cat .claude/hooks/kb-version 2>/dev/null || true)
  install="Install it: https://github.com/fabianvf/kb-compile#installing"
  [ -z "$version" ] || install="Install the version this repo pinned: go install github.com/fabianvf/kb-compile/cmd/kb@$version (or a prebuilt binary: https://github.com/fabianvf/kb-compile#installing)"
  deny "$(printf '%s\n\n%s\n\n%s' \
    "This repo uses kb-compile, but \`kb\` is not on PATH, so the commit gate cannot run." \
    "$install" \
    "Removing this hook is the other option. Exiting quietly instead would make every commit here look gated when none of them are, which is the failure this tool exists to prevent.")"
fi

if OUT=$(kb fresh 2>&1); then
  # Passing, but `kb fresh` also reports production source git cannot see and
  # no article claims - the state where a whole new subsystem is about to be
  # committed outside the KB's view. Discarding that here wasted the one
  # moment it is most actionable.
  #
  # stderr, and never a deny. Plain stdout from a PreToolUse hook is NOT added
  # to the model's context (see arch-gates), so there is no way to hand this to
  # the agent from here and pretending otherwise would be worse than silence.
  # The human running the commit is the one who can `git add`, and they see
  # stderr. Blocking on it is wrong: nothing is stale, and a gate that stops a
  # correct commit over an advisory is one people disable.
  case "$OUT" in
    *"no article claims"*)
      printf '%s\n' "$OUT" | sed -n '/^Also:/,$p' >&2
      ;;
  esac
  exit 0
fi

deny "$(printf '%s\n\n%s' \
  "The knowledge base is stale for source in this commit." "$OUT")"
