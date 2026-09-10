---
id: arch-links
type: arch
covers:
  - internal/kb/links.go
  - internal/kb/reverseindex.go
  - internal/repo/git.go
  - contrib/**
see_also:
  - arch-gates
---
# Test links and the reverse index

`.reverse-index.json` answers "I touched this file, what do I read and where do
the test updates go" as a lookup instead of a grep. The `articles` column comes
from ownership; this article is about the `tests` column, which is where all of
the language-specific machinery in this repo lives.

## INVARIANT

**Every language-specific line serves one column.** All nine enforcement
functions are language-agnostic. Run the tool with `adapters: []` and the
ratchets, freshness, path validation and edge validation are unaffected; only
`tests` empties. Adding a language must never become a prerequisite for the
gates working.

**`git ls-files` decides what the repo contains, never the filesystem.** A
build artifact left in a working tree would make the verdict differ between a
dev machine and CI, and a gate whose answer depends on the machine is worse
than no gate. This shipped broken once: a generated file left over from a local
test run made the check pass locally and fail in CI.

**A link command's output is validated, not trusted.** A script emitting
absolute, stale, or script-relative paths fails at the boundary rather than
producing links that point nowhere and look real.

## DECISION

**Prefer `link_commands` over regex adapters.** The adapters reimplement each
language's resolver approximately. `go list` knows the graph exactly, including
build tags, aliased imports and generated code. Measured on a 505-file Go repo:
`go list` found 8 tests for one file where the regex found 4, and correctly
found none for an entry point where the regex matched an unrelated program's
test. This repo uses `contrib/kb-imports-go.sh` on itself.

**A failing link command is fatal, and so is one that emits nothing.** The
rejected alternative, degrading to an empty column, is indistinguishable from
a repo with no tests. Silent-empty is the failure mode worth being loudest
about.

**Ambiguity yields nothing.** `underscoreSplit` reconstructs a flattened
pytest name only when exactly one candidate is a real tracked path. Guessing
produces a confident wrong answer: an agent opens the named test and finds it
guards something else.

**`match` on an extra link kind defaults to exact.** Prefix and substring exist
because a selector file's keys are often patterns, not paths. Reading patterns
as paths yields a silently EMPTY column, and it fails worst for the broadest
keys, so a whole subsystem's links vanish while the narrow ones look fine.

## GOTCHA

**A plain stem match needs the same directory or a distinctive stem.**
Universal filenames recur once per package. Requiring the same language was not
enough: Go has one `main.go` per command, so every entry point linked to every
unrelated entry-point test. Cross-directory matching survives only for
multi-word stems, which is what carries a client wrapper to its server-side
test - an edge no import can reveal.

**Package imports name a directory, so the link is to every file in it.** Go
and Java import a package and get all of it; there is no finer-grained truth
available, and claiming one file would be a guess. Deliberately not recursive:
a nested directory is a different package this import did not pull in.

**The reverse index is regenerated and compared byte for byte.** That is why
nothing in its construction may be non-deterministic, and why an LLM cannot be
in this path. Map iteration order is sorted everywhere for the same reason.

## VERIFY

`internal/kb/linkcommand_test.go` covers the command boundary including the
empty-output and untracked-path cases. `TestGoldenLinkKinds` pins each
mechanism by name against a case only that mechanism can produce, so one
silently dying cannot be masked by another covering the same slot.

## SEE ALSO

- [arch-gates.md](arch-gates.md) - shares the ownership map and the
  `IsTestFile` rule; a link command naming something a test that `test_rules`
  calls production source is rejected precisely so the two cannot drift.
