---
id: arch-links
type: arch
covers:
  - internal/kb/links.go
  - internal/kb/reverseindex.go
  - internal/repo/git.go
  - internal/imports/imports.go
  - internal/imports/scripts/**
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
test. This repo uses `kb imports go` on itself.

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

**A language whose tests live INSIDE the file they test has no edges to
report, and the fatal-empty rule turns that into a build failure.** Rust is the
case: the dominant convention is `#[cfg(test)] mod tests` in the same file, so
there are no two files and no link command can invent one. `rs.sh` therefore
only reports edges out of a crate's `tests/` directory, and a crate with only
inline tests produces nothing, which is the honest answer and is
indistinguishable, to `kb`, from a broken toolchain. The resolution is not to
soften the rule but to NOT configure the command for that repo; `stem_rules`
already recovers the `foo.rs` / `tests/foo.rs` pairing without claiming to have
read an import graph. The script says so in its own header, at length, because
the person who configures it is not the person who later sees it fail.


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
PLATFORM is part of that: `go list` reports only the files that build for the
current `GOOS` (checked 2026-09-11), so the Go resolver unions in
`IgnoredGoFiles`.
Without it a package with `x_linux.go` and `x_darwin.go` produced a different
index on each machine, and the CI matrix rejected an artifact its author could
not reproduce.

**A link command that over-reports is worse than one that under-reports.**
The reverse index is what an agent reads to decide which files to open, so a
spurious edge costs context on every lookup while a missing one costs it once.
The Dart script followed every directive of an imported library rather than
only its `part` files, which pulled in whatever that library imported and
multiplied a real Flutter app's index from 711 edges to 3212 - while reading,
from the outside, as thoroughness.

**Directory exclusion scopes OWNERSHIP, not existence.** `exclude_dirs` says
"these files need no article"; it does not say "these files are not in the
repo". The two were one thing - the exclusion was applied to the tracked-file
set at load - and that set answers both "what must be documented" and "is a
path an article cites real". So an article citing a test file from a `VERIFY`
section was told its own correct reference was dangling: on a repo excluding
its test directories, 154 false errors burying 8 genuine ones. The exclusion
now lives where ownership is decided - `IsProductionSource` for the orphan
ratchet, and the `covers:` glob expansion in `BuildOwnership`, so a broad glob
cannot silently take ownership of a vendored tree. An explicit `## FILES`
entry is deliberately NOT filtered: naming one path by hand is a claim about
that path, not a glob that caught it by accident.

**Directory exclusion matches whole path SEGMENTS.** A false positive there is
invisible rather than noisy: the file is simply never asked for. The field took substrings, and its own default list had to
carry both `vendor/` and `/vendor/` to cover the root and the nested case -
the segment rule, hand-rolled and wrong, because the unanchored spelling also
swallowed every directory whose name merely ended in `vendor`. The predicate
lives on `Config` and `repo.TrackedFiles` takes it as an argument, so the
inventory and `IsProductionSource` cannot come to disagree about a path.

**`git ls-files` needs `-z`.** `core.quotePath` is on by default (git 2.x,
checked 2026-09-11), so without it
a path containing any non-ASCII byte comes back C-quoted and octal-escaped:
`src/café.go` arrives as the literal `"src/caf\303\251.go"`. That matches no
prod root and no `covers:` glob, so the file is silently outside every gate,
while a link command reporting its real UTF-8 path is told git does not track
it. The same loop deduplicates, because a conflicted path is listed once per
merge stage and mid-merge is exactly when someone runs this.

**The inventory is tracked-only, and the one query that is not must never
change a verdict.** `TrackedFiles` is deliberately not a filesystem walk: a
build artifact left in a working tree would make a gate answer differently on a
dev machine than in CI, and an earlier checker learned that the hard way when a
generated file from a local test run made the check pass locally and fail for
everyone else. `UntrackedSource` asks the opposite question - `git ls-files
--others --exclude-standard` - purely so the gates can WARN that their own view
is narrower than the author's. It feeds no ratchet and no ownership map, so it
cannot turn a pass into a fail or back. `--exclude-standard` keeps gitignored
build output out of the answer, which matters more than it sounds: a warning
that names generated files is one people learn to skip, and this one is worth
keeping sharp.

**A link command runs under a deadline.** These commands are external
programs, and `kb graph` is called from a pre-commit hook, so an unbounded one
does not fail - it wedges the commit with no output and nothing to read. That
is the one failure a developer cannot diagnose and will work around by
disabling the hook, taking every other check with it. A timed-out command is
reported as timed out rather than as `signal: killed`, which would send the
reader to debug the script instead of the limit.

**The deadline is `WaitDelay`, not the context.** `CommandContext` kills the
direct child and `Output()` then waits for the stdout pipe to reach EOF, which
a grandchild that inherited it holds open. So a command whose shell forks ran
to completion whatever the deadline said: measured at 5.02s under a 150ms
deadline for `sleep 5 & wait`, against 150ms for `sleep 5`, where the shell
execs and the kill lands on sleep itself.

Which means it worked on macOS and did nothing on Linux, because `/bin/sh` is
bash there and dash here and bash execs a single command where dash forks. The
platform it worked on is not the platform the commit hook runs on, and the test
that caught it had passed for a month on the other one.

**The resolvers ship INSIDE the binary, not in the adopting repo.** They
were scripts to copy into `contrib/`, which put this tool's code in somebody
else's repository and decoupled the resolver from the checker consuming its
output - a repo could sit on a year-old copy and watch edges quietly diverge
from what the current kb expects. Embedded, they are versioned with the binary
and CI needs no step to fetch them. They stay shell-and-Python rather than
being rewritten in Go because that is the part most likely to need per-repo
tweaking, and Go is the hardest part for an adopter to tweak.

**A bare `kb` in a link command means THIS binary, not PATH.** `command: [kb,
imports, python]` is the documented spelling, and resolving it through PATH
broke every invocation by absolute path - which is how CI runs it and how `go
run ./cmd/kb` runs it - with "executable file not found" for a binary that was
executing at the time. Rewriting it to `os.Executable()` also makes the
embedding argument true rather than merely likely: the resolver producing the
edges is the one inside the checker reading them, not whichever `kb` is first
on PATH. A spelled path like `./kb` is the caller being specific and is left
alone.

**Only a list item or a table row inside `## FILES` claims a file.** The
section runs to the next heading, so a paragraph written under the table used
to claim every backticked path in it. That is not cosmetic: an owned file is
removed from the orphan ratchet, so mentioning a path while explaining the
table silently took it out of the gate - the one direction this tool must not
move a file unasked. SPEC §3 gives the block two shapes, a list and a table,
and prose is neither.

**A link command must not need its language's toolchain installed.** The
resolvers need `python3` and `git` and nothing else - not `pip
install -e .`, not `npm ci`, not a resolved Maven cache. A resolver that reads
an installed dependency tree answers a question about the MACHINE, so the
committed reverse index would differ between a fresh clone and a working
laptop, and `CheckReverseIndex` compares it byte for byte. That is the same
failure `git ls-files` exists to prevent, arriving through the language
instead of the filesystem.

**A link command's output is filtered against the tracked set.** `go list`
walks the filesystem; git decides what exists. One untracked scratch test file
otherwise turned the whole gate red, once per edge, with advice that was wrong
twice over.

**A `## SEE ALSO` section contributes EDGES, not just a boolean.** The parser
recorded `HasSeeAlsoSectionLink` and discarded the targets, so the section
satisfied "does this article have an outbound edge?" while contributing
nothing to "where does that edge go?". Anything that WALKS the graph saw an
empty graph on any KB declaring its edges in the section - which is the form
SPEC.md's own example uses. `Article.OutboundEdges()` is the union of both
forms, de-duplicated and self-excluding, and is the right call for traversal;
`SeeAlso` alone remains the frontmatter contract and the right field for
validating declared edges.

## VERIFY

`internal/kb/linkcommand_test.go` covers the command boundary including the
empty-output and untracked-path cases. `TestGoldenLinkKinds` pins each
mechanism by name against a case only that mechanism can produce, so one
silently dying cannot be masked by another covering the same slot.

## SEE ALSO

- [arch-gates.md](arch-gates.md) - shares the ownership map and the
  `IsTestFile` rule; a link command naming something a test that `test_rules`
  calls production source is rejected precisely so the two cannot drift.
