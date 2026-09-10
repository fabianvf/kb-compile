---
id: arch-gates
type: arch
covers:
  - internal/kb/article.go
  - internal/kb/checks.go
  - internal/kb/ratchet.go
  - internal/kb/fresh.go
  - hooks/**
see_also:
  - arch-links
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
every entry counts as growth and `--write` refuses; `KB_RATCHET_INIT=1` is the
one-time adoption path. Without this, `rm .orphans-baseline.txt && kb graph
--write` grandfathered anything, permanently and silently, and the read-only
error recommended that exact command. Absence is indistinguishable from
deletion, which is the same reasoning that makes a missing manifest a failure.

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

## GOTCHA

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
