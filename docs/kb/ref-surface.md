---
id: ref-surface
type: ref
covers:
  - internal/config/config.go
  - cmd/kb/main.go
see_also:
  - arch-gates
  - arch-links
---
# Config schema and CLI surface

Everything repo-specific is data. This article is the reference for what a
`.kb/config.yaml` may contain and what the commands do; the reasoning behind
each check lives in the article that owns it.

## FILES

| Path | Role |
|---|---|
| `internal/config/config.go` | The schema, defaults and validation |
| `cmd/kb/main.go` | Command dispatch and the failure output |

## COMMANDS

```
kb graph [--write]     validate; --write regenerates the derived artifacts
kb fresh               fail if KB-owned source moved since the last compile
kb compiled [ARTICLE...]
                       record a compile. With article names, records only what
                       those articles own. Never from a hook
kb read ID[#SECTION]   print an article, or one section
kb eval                score the article boundaries against git history
kb owners FILE...      the owning articles and their invariants, for one file
kb imports LANG        tab-separated test/source edges, for
                       a `link_commands` entry
kb task SUBCOMMAND     uncommitted working notes for one task: start, commit,
                       context, finish, list, drop. See arch-tasks
kb install             write the gate, hooks, skills and spec into .claude/ for
                       committing. Merges settings.json, and adds the
                       manifest's merge=union to .gitattributes. See arch-gates
kb version             the release stamped in at build time, else the module
                       version Go embedded, else `dev`. A version that cannot
                       identify a build - `+dirty` or `dev` - carries a hash of
                       the binary's own bytes

  --init               graph, with --write only: create a ratchet baseline
                       that starts non-empty. Refused once the baseline
                       exists, so it is an adoption step and not a way past
                       the ratchet. Refused without --write rather than
                       silently doing nothing
```

## COMMANDS

```
KB_COMPILE_OK=1        `kb fresh` waives staleness. For a revert or a pure
                       rename; typing it often means the check is wrong
```

**The KB is at `docs/kb/`, not `.kb/`, and the split from the config is
deliberate.** Putting articles beside the config in `.kb/` is the tidier
layout and it was rejected: ripgrep, `fd` and most editor search skip hidden
directories unless asked (verified against ripgrep, 2026-09-11), so
`rg "eviction order"` finds an article under
`docs/kb/` and silently misses the identical one under `.kb/`. That is the
search an agent reaches for precisely when it does not already know the
article exists, which is the case the KB is for. `.kb/` holds machine config
nobody greps for; `docs/kb/` holds prose two audiences read.

**`path_extensions` must be a SUPERSET of `prod_extensions`, and that is now
enforced at load.** The prose scanner only recognises a path whose extension is
listed, so a prod extension missing from it meant an article could cite a
production file by name and have the citation silently unchecked - the one
guarantee this tool sells. The default makes the two equal; an explicit
narrower list did not, and nothing said so.

**`link_commands` with no `test_rules` cannot be satisfied, so it fails at
load.** A link command's output is validated against `test_rules`; `TestRules`
has no default, so `IsTestFile` is false for everything and EVERY edge the
command emits is rejected - reported as a broken toolchain. Two independent
first-time adopters hit this, both because the config guidance implied defaults
existed.

**`kb version` never invents a number it cannot substantiate.** It defaults
to the empty string rather than a plausible-looking `0.1.0-dev`, and falls
back through the `-ldflags` stamp, then the module version Go embeds for a
`go install ...@<tag>` build, then `dev`. kb-init tells teams to pin the
version so the checker cannot change behaviour under them, and a binary that
misreports which one it is makes that instruction unfollowable. The release
workflow asserts the stamped value matches the tag before publishing.

**A gate bypass is a flag, not an environment variable, unless it has to
survive a hook.** `--init` has to be typed at the command that uses it; an
exported variable follows every later run, and the run it would change is the
one where the baseline is ABSENT - indistinguishable from deleted, which is
the case the guard exists to refuse. `KB_COMPILE_OK` stays a variable because
the thing it waives runs from a commit hook, where there is no command line to
add a flag to.

## INVARIANT

**Failure output is written as instructions, not diagnostics.** Not every agent
reading these messages will have the KB's conventions loaded, and some cannot.
The error has to carry the fix. This is also what makes the tool
editor-agnostic: an agent that never loads a skill is still stopped by the
binary and told what to do.

**An OK line states the claim being recorded, not the bookkeeping.** Bare
`kb compiled` used to print the file count, which is the part nobody can get
wrong. It now names how many ARTICLES the record asserts were reviewed, against
how many files actually changed, and says outright that the record is false for
any of them you did not open. `kb fresh` prints the scoped command for exactly
the articles it listed, and `kb compiled ARTICLE...` reports what remains
unreviewed rather than only what was taken.

**A file is recordable only when EVERY article that owns it was named.** The
manifest is keyed by path and nothing else, so writing a hash attests that file
to all of its owners at once. Scoping the record without this made the honest
act the damaging one: `kb fresh` lists every owner and says "drop the ones you
did not read", and for a co-owned file dropping a name recorded the file
anyway, clearing the dropped article and turning the gate fully green on an
article nobody opened. Held back, the shared file stays stale and both articles
stay red, which is the truth - the only evidence either way is a file whose
review is unfinished. `kb compiled` says which files it held and which articles
it would otherwise have attested, because "recorded 0 file(s)" on its own is
indistinguishable from a no-op.

**The advisory scan may never change a verdict, including by dying.** The
untracked-source warning is the one query in the tool that is not part of any
gate, and `die` on its error broke that: `git ls-files --others` exits 128 on a
`core.excludesFile` that `git ls-files` alone tolerates, so `kb fresh` went
from 0 to 2 and the commit hook denied every commit in the checkout with "the
knowledge base is stale" over a git config error. The failure is now printed
and the reports omitted.

**Flags must precede the article names.** Go's `flag` stops parsing at the
first positional, so `kb compiled arch-api --root /repo` left `--root`
unapplied - running the command against the wrong repository - and then
reported `--root` as a misspelled article, naming neither the problem nor the
fix. Any residual argument starting with `-` is refused by name. This exposure
is new: before `compiled` took positionals, flag order could not matter, and
`kb fresh` now actively trains people to type a positional form.

**`kb version` must be able to identify the build it came from.** The Go
toolchain derives `+dirty` from WHETHER a tree has uncommitted changes, not
from their content, so every build from one working tree reported an identical
string. That made "tell me if the binary looks stale" unanswerable except by
running it and recognising behaviour - available only after you have already
relied on it, which is the shape of every failure in this repo. The digest
hashes the executable rather than the tree, so a plain `go build` gets the same
guarantee as a scripted one. A tagged build gets no suffix: `kb-init` tells
teams to pin a version, and appending bytes to one would break that.

**Every allowlist entry requires a written reason.** `not_repo_paths` and
`generated_paths` reject empty values. An entry without a reason is
indistinguishable from a mistake, and this is the argument that decided YAML
over JSON: the reason needs somewhere to live.

## DECISION

**YAML by default, JSON still accepted, both is an error.** The format is an
implementation detail of one schema - YAML is converted to JSON before decoding
so a single set of struct tags governs both and unknown-key rejection is
identical. Finding two configs is an error rather than a precedence rule,
because two means one is stale and silently preferring either lets an edit land
in the file nobody reads.

**Unknown keys are rejected.** A typo'd `covers:` silently owns nothing, which
is exactly the class of failure this tool exists to catch.

**The untracked-source scan is lazy, and that is load-bearing rather than
tidy.** `git ls-files --others --exclude-standard` stats the whole working
tree. Only `graph`, `fresh` and `compiled` read the answer, and `owners` runs
from the `UserPromptSubmit` hook on every message a human sends - it is the one
command sold as a cheap lookup, and an eager scan made it walk the tree for a
result it never reads. Collapsing the `sync.OnceValue` back to a plain call at
setup is the obvious cleanup and it puts the cost back.
`TestOwnersDoesNotWalkTheWorkingTree` shims `git` onto PATH and fails if
the call reappears, with `kb graph` as the control so a broken shim cannot
pass as a fixed bug.

**An explicit `exclude_dirs` REPLACES the defaults.** Vendored trees are
excluded by default so adoption does not begin with thousands of orphans from
code nobody wrote, but a repo that genuinely documents its vendor tree must be
able to say so without fighting a merge.

**`exclude_dirs` excuses a file from needing an article; it does not hide the
file.** Excluded paths stay in the tracked set, so an article may cite one -
a `VERIFY` section naming a test is the common case - without the link gate
calling it dangling. What the exclusion does reach is ownership: the orphan
ratchet skips those files, and a `covers:` glob will not expand into them.

**`exclude_dirs` matches whole path segments, and only directory ones.** It
took substrings, and the default list carried both `vendor/` and `/vendor/` to
cover the root and the nested case - the segment rule hand-rolled, and wrong:
the unanchored spelling also matched any directory whose name merely ENDED in
`vendor`, dropping real source out of the inventory so that no gate ever saw
it. The predicate lives on
`Config` and `repo.TrackedFiles` takes it as an argument, so the inventory and
`IsProductionSource` cannot disagree about a path.

## GOTCHA

**A prod root is a path prefix, not a glob, and an empty scope is refused.**
`prod_roots: [app/]`, `[./app]` and `[app/**]` all matched nothing, which
emptied the orphan ratchet's scope while the run still reported OK. The
unambiguous spellings are normalised and a glob is now an error. Same for
`prod_extensions`: an empty list made `IsProductionSource` false for every
file, so it fails at load the way an empty `prod_roots` already did.

**An `exclude_dirs` entry containing `/` or `*` is refused at load.** Every
config written against the old substring field spells these `vendor/`, and a
slash matches no segment - so accepting it would quietly un-exclude a vendored
tree and surface as thousands of new orphans rather than as a config error.

**A pattern's capture groups are validated at load.** Every adapter reads group
1 and every `extra_link_kinds` entry reads groups 1 and 2, unconditionally, so
a pattern with fewer used to panic with a Go stack trace on the first matching
file instead of failing as the config error it is.

**`SourceExtensions` defaults to the union of adapter extensions.** A test file
outside that set is skipped by the link builder entirely, which is correct for
a PNG fixture parked under a test directory and surprising if you configure
`link_commands` with no adapters and expect stem matching to still run.

## SEE ALSO

- [arch-gates.md](arch-gates.md) - reads `test_rules`, `prod_roots` and both
  allowlists; changing what counts as a test changes the ratchet's scope.
- [arch-links.md](arch-links.md) - owns the adapter and `link_commands`
  semantics these fields configure.
- [arch-tasks.md](arch-tasks.md) - `kb task` dispatches before the graph pass
  and parses its own flags, so moving it below the checks breaks its rule.
