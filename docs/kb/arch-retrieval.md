---
id: arch-retrieval
type: arch
covers:
  - internal/kb/digest.go
  - internal/kb/read.go
  - internal/kb/eval.go
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

**The digest header quotes its own size, so it can only be written after the
body.** Estimating it first produced a number wrong by half, in the one place a
reader decides whether to trust the other numbers.

**`kb eval` measures the carve, not the content.** A KB of perfectly-bounded
articles full of fluent restatements of the code scores well. Measuring whether
the prose is any good still needs a model in the loop, and nothing here does
that.

## VERIFY

`TestDigestIsSmallerThanTheCorpus` asserts the premise numerically rather than
assuming it: a digest that is not an order of magnitude cheaper than the
articles has no reason to exist. `internal/kb/digest_test.go` covers all three
claim shapes and the editorial-aside filter.

## SEE ALSO

- [arch-gates.md](arch-gates.md) - the digest is generated and verified by the
  same `kb graph --write` pass as the ratchets, so a stale digest fails the
  same build.
