---
id: arch-gates
type: arch
covers:
  - internal/kb/article.go
  - internal/kb/checks.go
  - internal/kb/ratchet.go
  - internal/kb/fresh.go
  - internal/kb/install.go
  - hooks/**
see_also:
  - arch-links
  - arch-tasks
  - ref-surface
---
# The two gates

`kb graph` asks whether the KB's edges RESOLVE. `kb fresh` asks whether anyone
LOOKED at it. They are separate commands because they fail differently, and
only the second one produces a confidently-wrong article, which is the failure
that actually costs something: an agent is told to read the KB first, so it
acts on stale text instead of reading the code.

## INVARIANT

**`kb fresh` runs the graph pass first and bails if it fails.** A freshness
verdict computed from a broken ownership map is worse than no verdict: it
reports a clean KB while articles point nowhere. `cmd/kb/main.go` runs the
graph checks unconditionally for exactly this reason, and both `fresh` and
`compiled` return early on any error.

**Ownership is derived once and shared.** Both gates use the same
`BuildOwnership` result. Deriving it twice from two code paths is how the two
would come to disagree about what the KB owns, and the disagreement would be
invisible in both.

**A ratchet may shrink and never grow.** `writeRatchet` refuses a `--write`
that would add entries. This is the whole mechanism, not a safety rail: if
regenerating could widen a baseline, then adding an undocumented file and
re-running `--write` silently legitimises it. The gate keeps passing and stops
gating, which is worse than failing because nobody looks at a green build.

**A missing baseline is not permission to start over.** It reads as empty, so
every entry counts as growth and `--write` refuses; `--init` is the
one-time adoption path. Without this, `rm .orphans-baseline.txt && kb graph
--write` grandfathered anything, permanently and silently, and the read-only
error recommended that exact command. Absence is indistinguishable from
deletion, which is the same reasoning that makes a missing manifest a failure.

**An article git does not track is not part of the KB.** Every inventory in
this tool comes from `git ls-files` except one: the article loader reads the
KB directory off the filesystem. So an article nobody had `git add`ed passed
every check locally, while the DIGEST and reverse index beside it - which are
committed - went on naming it. Both of the first two repositories to adopt
this shipped exactly that, because the pre-commit hook staged the four
generated files and nothing else: a commit whose derived artifacts described
seventeen articles the repo did not contain, and whose `kb graph` failed for
everyone but the author.

The check is gated on the KB having been committed at all, because the two
states mean different things. A wholly untracked KB is somebody writing their
first articles and is not yet a contract with anyone; a PARTLY tracked one is
a broken commit that has already happened or is about to.

**Production source git does not track is outside every gate, and all three
say so.** Ownership is built from `git ls-files`, so a subsystem written and
not yet staged has a `covers:` glob matching nothing and a compile marker
recording a narrower set than its author believes. The narrowing is silent and
looks exactly like success. `kb graph` names those paths; `kb compiled` names
them again at the moment the judgement is written, scoped to the articles being
recorded so a peer session's work in progress is not reported as yours; and
`kb fresh` reports the subset that no article claims at all.

That last report covers the one cell nothing else reaches.
`CheckOrphanRatchet` walks the tracked list too, so a file that is both
untracked and unowned is invisible to the ratchet AND to the ownership map.
That is not an edge case: an article and the code it covers land in one commit,
so during the window the code is unstaged, no article claims it. It is the
normal birth condition of every new subsystem. `Article.Owns` exists for this
and reads the `covers:` globs and `## FILES` block directly, because the
ownership map is built by applying them to the tracked list and therefore
cannot answer for a path git has never seen.

The warning never fails a build. A dirty tree is the normal state mid-work, and
a gate people route around asserts less than one that nags.

**The commit gate surfaces the OK-path advisory on stderr, and cannot do
better.** `kb fresh` passing still reports production source git cannot see
that no article claims, and the hook used to discard it at the one moment it is
most actionable. It goes to stderr rather than stdout because plain stdout from
a `PreToolUse` hook is not added to the model's context, so there is no way to
hand it to the agent from here and formatting it as though there were would be
worse than silence. The human running the commit is the one who can `git add`,
and they see stderr. It never denies: nothing is stale, and a gate that blocks
a correct commit over an advisory is one people switch off.

**Context is pushed on `UserPromptSubmit`, because that is the only event that
can push it.** Plain stdout from a `PreToolUse` hook is NOT added to the
model's context, and `additionalContext` is documented only for
`UserPromptSubmit` (checked 2026-09-17). Editing is where the knowledge matters
most and `PreToolUse` is where you would want to intervene, so the delivery
lands at the next best moment instead: every time the human says something.

The digest goes in once per session, keyed on `session_id`. Repeating it would
spend its whole token budget on every prompt to say nothing new, which is how a
helpful hook becomes one people disable. Paths named in the message get
`kb owners` output attached every time, because that half is small and is
different each prompt.

**The granularity of the RECORD matches the granularity of the CHECK.**
`kb fresh` names articles one at a time; `kb compiled` accepts article names
and records only what they own. It used to record every KB-owned file at once,
because per-file recording lets you assert coverage you do not have until the
gate is green and asserts nothing.

That reasoning is sound and it produced a failure it did not anticipate. An
agent that had genuinely reviewed three of the five articles the gate named,
and genuinely not reviewed the other two, had exactly one truthful move: record
nothing, leave the gate red. Which is the output a careless agent produces too.
The gate could not separate them, so honesty cost the honest agent everything
and bought no information. The anti-gaming argument does not call for
coarseness - it calls for each claim to be specific and attributable, which is
the opposite of coarse.

Scoping alone leaves the cheap path and the honest path pointing opposite ways,
so `kb fresh` ends by printing the scoped command pre-filled with every article
it just listed. Bare `kb compiled` is the shortest thing to type in the tool:
pasting that line attests to exactly what the gate named, and attesting to less
means deleting article names from it. Neither is a correctness guarantee. It
moves the typing cost onto the wrong answer instead of the right one, which is
the only lever available - the tool cannot tell a read article from an unread
one and never will.

The claim is also only ever as fine as the widest glob you name. An article
covering a directory is attested whole, so `kb compiled arch-x` after reading
two of its five changed files is precisely wrong about a smaller thing rather
than vaguely wrong about a big one. That is the honest cost of the grain.

**A gate that cannot run must say so, not stand down.** The commit hook
tests for its config FIRST, and everything after that point is a broken
install rather than a reason to exit quietly: past the config test the repo
demonstrably uses this tool. Each missing piece used to be a silent `exit 0` -
the wrong config filename, then `kb` absent from PATH, then `jq` absent - and
the second of those is the default state right after installing the plugin,
because the plugin cannot install the binary. Every commit then looks gated
while none of them are.

**The bootstrap hatch is a FLAG, not an environment variable.** It was
`KB_RATCHET_INIT=1`, and the one property a bypass must not have is the
ability to be set far away from the command it affects. An exported variable
follows every later run, and the run it would change is the one where the
baseline is absent - which is indistinguishable from deleted, the case this
guard exists to refuse. `--init` has to be typed at the call site, `--help`
lists it, and the tests no longer have to clear an ambient value that had made
them fail accusing the code of a bug the shell had caused. `KB_COMPILE_OK`
stays a variable because what it waives runs from a commit hook, where there
is no command line to add a flag to.

**The bootstrap hatch only applies when the baseline is ABSENT.** Honouring
`--init` against a file that is already there makes it the `rm`
bypass with one more token to type — and worse, both refusal messages print
that command, so the tool would be advertising it. Gating on absence also
scopes it correctly when one baseline is missing and the other is healthy: a
single `kb graph --write` writes both, so an unscoped hatch would grandfather
every dead end while the reader was adopting the orphan ratchet.

**Only ENOENT reads as absence.** `readBaseline` returns everything else as
the error it is. Collapsing a permission bit or a directory-at-the-path into
"missing" told the reader to restore a file that was sitting right there, and
offered them the bootstrap hatch for a baseline nobody had deleted.

**A conflicted baseline is refused, not parsed.** The baselines are one entry
per line so that they merge, which means they conflict for the same reason the
manifest does. Read as entries, the markers make `existing` the UNION of both
branches, so `--write` accepts an entry only one side grandfathered — and the
read-only error said to run `--write`, which resolved the conflict silently.
Detection is shared with the manifest and matches every marker, because
half-resolving by hand and leaving the tail behind is the usual way to arrive
here.

**The freshness manifest is never regenerated automatically.** Not by a hook,
not by CI, not bundled into another target. It records a judgement, and a
judgement regenerated on its own asserts nothing. `hooks/kb-fresh-gate.sh`
runs `kb fresh` and never `kb compiled`; that distinction is the difference
between a gate and a formality.

## DECISION

**Freshness state is per file, not a commit stamp.** The rejected design was a
single `last_compiled_commit` diffed against the tree, which made every MERGE
look like drift: bringing trunk into a branch changes the tree, so the branch
owed a compile for files reviewed on the other side. Per-file hashes merge the
way git merges everything else, and two branches compiling the same file
conflict correctly because one of their reviews is about to lose.

**A conflicted manifest names the situation and rules out the obvious fix.**
Per-file state means two branches compiling the same file conflict on purpose,
so hitting one is normal rather than exceptional. Resolving it with `kb
compiled` would re-record every file on both sides as reviewed, including the
ones nobody looked at, which is the one use that defeats the gate.

**A missing manifest FAILS rather than skipping.** The rejected alternative,
treating absence as "nothing to check", makes `rm .compiled-sources.json` a
one-line bypass of the whole gate. Same shape as a test suite that passes
because it found no tests.

**Section-anchor drift warns rather than fails.** The `§ SECTION` convention
is written loosely enough that hard-failing would mean rewriting prose doing
its job. Absence of an edge is checkable; quality of a citation is not.

**The dead-end ratchet checks absence, never quality.** An edge must name an
obligation rather than a topic, and no checker can tell those apart. Enforcing
what is checkable and documenting what is not beats a rule that pretends.

**A team gets the hooks by committing them, not through the plugin.** Per the Claude
Code docs (checked 2026-11-01), enabling a plugin in a committed settings file does
not download it, so each collaborator must run an install nobody told them about,
and the gate is silently absent until they do. `kb install` writes copies into
`.claude/` instead, from the one set `assets.go` embeds, and re-running it replaces
them. Tested 2026-11-01 on a fresh clone with no plugin, under
`claude -p`: the digest reached the model and the gate script denied a stale commit.
The interactive trust dialog was not tested. This repo itself has not run `kb
install`, so nothing gates a commit here without the plugin.

**The prompt hook prints a task's resume view once per session, then one line.**
`kb task context` asks git about the working tree and costs up to 1.5k tokens, so
running it per prompt would pay both on every message, the cost
`TestOwnersDoesNotWalkTheWorkingTree` keeps out of `kb owners`. The one line reads
only `.kb/tasks/CURRENT`, and it is what reaches an agent after compaction, which
the session marker survives and the resume view does not.

## GOTCHA

**A heading inside a fenced block is an example, not a section.** Read as a
section it is a free pass past both ratchets: a fenced `## SEE ALSO` sample
clears the dead-end gate, and a fenced `## FILES` sample claims ownership of
whatever paths it shows, erasing them from the orphan gate. Any heading level
closes the section it was in; only `## ` opens one, so a `### ` subhead no
longer leaves `## FILES` open over the prose beneath it.

**`covers:` accepts every YAML list spelling, and a scalar is an error.** A
zero-indent `- item` and an inline `[a, b]` are both valid YAML and both used
to parse to nothing, so a correctly-spelled `covers:` owned no files and they
landed in the orphan baseline. That is the same silent no-op that rejecting
unknown frontmatter keys exists to prevent, so it fails the same way.

**A `## FILES` table is an ownership claim, exactly like a fenced block.**
Reading only the fence silently dropped every table-shaped article's claims,
and those files landed in the orphan baseline as though nothing documented
them. But a bare filename in a table cell is deliberately NOT a claim: it
cannot be resolved unambiguously, so honouring it would let an article claim a
file that does not exist.

**A `## SEE ALSO` link to the article's own id is not an outbound edge.**
Without that check the cheapest way past the dead-end ratchet is to link an
article to itself: the section exists, it holds a link, the gate is satisfied,
and the reader ends up where they started. Frontmatter `see_also` always
rejected self-edges; the section path did not, and it took running `kb-init`
against an unfamiliar repo to notice.

**`article_types` is optional and its absence disables `type` entirely.** A
repo adopting this should not have to rename every existing doc. Configure it
and it is enforced; omit it and articles may be named anything.

**The section vocabulary is warned on, not enforced.** SPEC.md calls the list
fixed and nothing checked it, so the guarantee was a sentence in a document.
The cost is not tidiness: `DIGEST.md` and `kb owners` render only `INVARIANT`
and `GOTCHA`, so a claim under any other heading cannot reach an agent
choosing what to read. A 36-article KB grown over five months had 73
`## PATTERN` sections against 14 `INVARIANT` and 18 `GOTCHA` - 87% of its
claims unreachable, every gate reporting it clean. `CheckSectionVocabulary`
warns rather than errors, because an existing KB should not be bricked by a
rule it predates, and it collapses `## PATTERN: a` and `## PATTERN: b` to one
finding so 73 variants of one mistake are not 73 lines.

## VERIFY

`internal/kb/judgement_test.go` pins each of the above, and each fails if its
rule is inverted. `TestGoldenDetectsBreakage` proves the checks fire at all: a
gate nobody has seen fail is a gate nobody knows works.

## SEE ALSO

- [arch-links.md](arch-links.md) - supplies the test links that share this
  file's ownership map, so a change to what counts as a test file changes both
  the ratchet's scope and the index.
- [ref-surface.md](ref-surface.md) - owns the config fields these checks read,
  including `test_rules`, which the ratchet and the link builder must agree on.
- [arch-tasks.md](arch-tasks.md) - task notes must stay out of both gates, so a
  change that feeds one into `kb fresh` or the manifest breaks that rule.
