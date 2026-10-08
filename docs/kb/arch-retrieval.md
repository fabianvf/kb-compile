---
id: arch-retrieval
type: arch
covers:
  - internal/kb/digest.go
  - internal/kb/read.go
  - internal/kb/eval.go
  - internal/kb/owners.go
see_also:
  - arch-gates
---
# Reading the KB without loading it

The consumer is an agent with a context budget. On a real 35-article KB the
corpus is ~270k tokens, the median article ~6k, the largest ~24k, and a hub of
links tells an agent every article's NAME and nothing about what any of them
knows. `DIGEST.md` and `kb read` exist to make that arithmetic survivable;
`kb eval` exists to check whether the articles are carved usefully at all.

## INVARIANT

**The digest is DERIVED, and that is what makes it trustworthy as a routing
table.** A hand-maintained summary drifts, and a drifted routing table sends
agents confidently to the wrong place, which is worse than no summary. It is
regenerated and byte-compared exactly like the reverse index.

**Every claim shape WRAPS, and the join is not cosmetic.** The parser reads
one physical line at a time, so a hard-wrapped claim was cut at the wrap in all
three shapes - the exact dangling fragment the `firstSentence` call beside it
was added to eliminate, arriving by the other door. Four claims in this repo's
own DIGEST and one in the test fixture were truncated that way and nothing
failed, because the artifact is generated and byte-compared against itself:
both sides agreed on the same wrong text. A gate that regenerates what it
checks cannot see a wrong generator.

`logicalLine` folds continuations in ONCE, after every structural test and
before the three claim branches, so no branch has to know about wrapping. The
first version of this fix was bold-only, which left plain bullets and
paragraphs still cutting: fixing one shape of a shape-independent bug is how
the other two stayed invisible for another round.

**The join's boundary set is what keeps the cure from being worse than the
disease.** `logicalLine` stops at a blank line, a heading of any level, a fence,
the next list item and a table row. The list-item stop is the one that bites:
`- **Subject.** explanation` bullets are this KB's dominant claim shape, so a
version that stopped only at blank lines, `## ` and a fence let one
unterminated `**` consume the following bullets into its own claim AND delete
them as claims of their own - four bullets came back as two, one a run-on of
the first two. Swallowing is strictly worse than truncating, and just as
silent. The boundaries here are deliberately looser than the enclosing loop's:
that loop tests `## ` and an unindented fence because it is deciding what a
line IS, while this one is deciding whether a line could be the tail of a
sentence, and `### Sub` or an indented code block is not.

**There is no wrap DETECTOR any more, and that is the point.** An earlier
version decided per line whether a wrap needed joining, which meant a second
notion of what a bold lead looks like. It was hand-rolled as
`TrimPrefix(t, "- ")`, narrower than the `[-*]\s+` the extractor accepts, so
`-  **Subject` and `-\t**Subject` were bold claims to one and not the other:
the detector skipped them, the line fell to the paragraph branch, and the claim
truncated anyway - the very bug the join exists to fix, still live for the
alignment-padded spelling.

`logicalLine` joins unconditionally, so no second notion exists to drift.
`boldOpenRe` survives inside it for one narrow job, stopping the join at the
closing `**` so `**A.** B. C.` does not pull the next paragraph in, and it is
the opening half of `boldClaimRe` rather than a separate spelling of it. Any
change that reintroduces a should-I-join test reintroduces this whole class.

**An article that yields no standard claims falls back to listing its
headings.** Otherwise an article using its own section vocabulary appears as a
name and nothing else, which reads as "this article says nothing" rather than
"the digest cannot see inside it". The two are opposite signals and the digest
must not confuse them.

## DECISION

**Claims are capped at three per section, 150 characters each.** The digest
routes, it does not replace reading. An article with fifteen invariants would
otherwise dominate and push the digest toward the cost it exists to avoid.

**`kb eval` reports recall against a directory baseline, not precision.** The
first version reported precision against a single commit. That was
miscalibrated: an article covers a subsystem and a commit touches part of one,
so the number can never approach 1.0 and measures granularity rather than
quality. It read 0.06 on a healthy KB. The control is what makes a recall
number a result at all: a KB at or below the directory baseline has carved
nothing the tree did not already carve.

**Recall is averaged over every choice of entry file.** Which file an agent
happens to touch first is arbitrary, and a KB should not score differently
depending on it.

**The verdict weighs recall AND reach, as recall per file read.** Comparing
recall alone called a KB with equal recall for half the reading "filing rather
than describing", which is backwards: that KB is strictly better. Efficiency is
what an agent actually spends. The verdict also refuses to conclude much below
30 qualifying commits, because a handful is a coincidence rather than a
measurement.

**`max_article_tokens` warns rather than fails.** The right length is a
judgement, and a build failing on prose length would just get the threshold
raised until it stopped firing.

## GOTCHA

**Real articles use three claim shapes and the parser must handle all of
them.** Bold-lead bullets, plain bullets, and prose paragraphs. Reading only
paragraph starts captured the FIRST bullet of a plain list and nothing else,
with its `- ` marker still attached.

**Both line scanners skip fenced blocks, and for the same reason.** A `## `
line in a code sample is an example. `extractClaims` read the CONTENTS of a
fence as prose and bullets, so two lines of YAML became headline claims and
evicted the article's real ones past the per-section cap; `ReadArticle` treated
it as a heading, which truncated the section actually being read and served the
code block back as a section the article does not have.

**A section is resolved before any text is emitted.** Matching per line meant
every heading that matched contributed its body, so `kb read id#I` returned
INVARIANT and IMPLEMENTATION glued together with nothing to say so, and a
single empty `## ` heading matched every query. Exact first, then a unique
prefix; ambiguity is an error naming the candidates.

**A digest is built from a COPY of the article's edges.** Sharing the slice let
the `## SEE ALSO` merge append into its spare capacity and the sort reorder it
in place, so building the digest silently rewrote `Article.SeeAlso` — dropping
a declared edge, and making a second build render a different digest.

**The digest header quotes its own size, so it can only be written after the
body.** Estimating it first produced a number wrong by half, in the one place a
reader decides whether to trust the other numbers.

**Every branch of the eval verdict must be reachable, because the verdict is
the sentence people act on.** The switch weighed recall against efficiency but
had no branch for materially worse recall at a lower reading cost, so that case
fell through to the default and told the reader the KB scored "about the same
on both" while it was missing most of every change. A wrong number invites
checking; a wrong sentence does not.

**The two halves of the coverage ratio come from the same commits.** `Unowned`
counted while walking every commit, but `Files` only over the commits that
qualified for scoring, so the report asked the reader to judge one against the
other while they were drawn from different populations. It overstated how
little of the repo the score covers, and the response to that number is to
distrust a score that was fine.

**The digest carries INVARIANT and GOTCHA, not DECISION, because it ROUTES.**
Its own header says "read this to decide what to open". DECISION answers why
the code is shaped as it is, which matters once the file is open. Measured on
20 localisation tasks against a repo with a real KB: routing on instructions
alone reached the fix's file 57.5% of the time at R@25, against 47.5% with
DECISION included - so the overview claims diluted rather than merely failed to
help. Volume held equal across five seeds, instructions scored about twice
overviews at every k. Matches Gloaguen et al. (arXiv:2602.11988) on agent task
success, which is a different measurement reaching the same split.

The honest caveat: this optimises for what `bench/route.py` can see. DECISION
may carry value for writing a good change rather than for finding the right
file, and nothing here measures that.

**An artifact nobody queries is worth nothing, so the lookup is a command.**
The reverse index always answered "I am editing this file, what do I need to
know" and the only way to ask was a `jq` expression against a generated file.
Measured: two agents given a real bug, in a repository whose `AGENTS.md` tells
them to look a file up before changing it, both went straight to grep, neither
opened the KB, and both produced fixes violating an invariant the owning
article states in its first two lines. `kb owners FILE` is that lookup.

It prints INVARIANT and GOTCHA and deliberately not DECISION. The question at
the moment of editing is "what will I break"; DECISION explains why the code
is shaped as it is, which is worth reading and is not that. An answer long
enough to skim is an answer that gets skimmed.

**`kb eval` follows one `## SEE ALSO` hop, and only one.** It measured a
PARTITION: reach was the files of the entry's own articles and nothing else,
so the edge - the one affordance a KB has that a directory tree does not -
earned zero. Two consequences, both seen on real repositories. A KB beating the
tree at matched reading cost reported as losing, because 5 files read was
compared against 22. And merging became the only lever the number could see,
so the advice pushed it even where better edges were cheaper and better.

Exactly one hop: a reader follows the edge they were given a reason to follow.
Transitive closure would credit a walk nobody takes and converges on "the whole
repo" in any well-connected graph. Both directions, because being named by an
article is the same reason to open it as naming one - following edges one way
only scores 0.75 where both ways scores 1.00, since entry from the article that
declares no edge finds nothing.

**The worst-case list is scored at one hop too**, because that is what a reader
actually fails to find. Scored at zero it named changes the edges already
covered, and its stated remedy - merge these articles - would have destroyed a
working carve to fix a number measuring the wrong thing.

**`kb eval` ignores paths git no longer tracks.** A deleted file can neither
be owned nor be a coverage gap, and counting it as unowned reported a
documentation hole on a repo whose orphan baseline was ZERO - 32 touches of
files that had been removed. The reader cannot tell that from the number, and
the line invites them to read it as a small documented corner.

**`kb eval` measures the carve, not the content.** A KB of perfectly-bounded
articles full of fluent restatements of the code scores well. Measuring whether
the prose is any good still needs a model in the loop, and nothing here does
that.

**Eval's one-hop expansion walks `OutboundEdges()`, not `SeeAlso`.** Walking
the frontmatter field alone measured the declared contract rather than the
graph a reader can traverse. On a 36-article KB with 35 `## SEE ALSO` sections
and zero `see_also:` keys, that is a graph with no edges: eval reported
one-hop recall identical to zero-hop and rendered a confident "your edges do
nothing" verdict about edges it could not see. Reading both forms, the same
KB scores recall 0.56 and 0.74 at one hop.

## VERIFY

`TestDigestIsSmallerThanTheCorpus` asserts the premise numerically rather than
assuming it: a digest that is not an order of magnitude cheaper than the
articles has no reason to exist. `internal/kb/digest_test.go` covers all three
claim shapes and the editorial-aside filter.

## SEE ALSO

- [arch-gates.md](arch-gates.md) - the digest is generated and verified by the
  same `kb graph --write` pass as the ratchets, so a stale digest fails the
  same build.
