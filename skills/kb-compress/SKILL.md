---
name: kb-compress
description: Periodically compress the knowledge base - deduplicate repeated content, collapse decision logs into the rules they imply, merge or split articles whose boundaries have drifted, and strengthen weak edges. Complements kb-compile, which keeps the KB CORRECT; this keeps it DENSE and therefore read. Run quarterly or when articles have grown long.
allowed-tools: Bash, Read, Write, Edit, Glob, Grep
---

Make the KB shorter without making it dumber.

Read [`SPEC.md`](../../SPEC.md) first. `kb-compile` keeps the KB *correct*;
this keeps it *dense*, which is what keeps it read.

**Cost:** every article gets read in full, so this runs roughly 5-15k tokens
per article plus the edits, meaning **100-400k for a 25-article KB**. Cheaper
than `kb-catchup` and still worth naming before you start. It is a periodic
pass, not something to run on every change.

---

## Why length is a correctness problem

A KB decays toward length. Every compile adds; almost none subtract. Articles
accumulate decision logs that should have become one rule, and the same
invariant gets restated in three places.

That is not merely untidy. It has two failure modes that look like correctness
bugs:

- **Duplicated content drifts.** An invariant stated in three articles gets
  updated in one. Now two articles are confidently wrong, and there is no way
  to tell which is current.
- **Long articles get skimmed.** An agent under context pressure reads the
  first third. Everything after that is, functionally, not in the KB - while
  still costing a review on every compile.

Compression is therefore about *what survives being read*, not about byte
count.

---

## 1. Survey

```bash
wc -l docs/kb/*.md | sort -rn | head -20
```

Longest first, but do not treat length as the target - a long `ref-` article
that is one big table is fine. What you are hunting is:

- the same fact in two articles
- a `DECISION` section that is a chronological log rather than a rule
- an article whose invariants split cleanly into two unrelated groups
- an article nobody could state the point of in one sentence

Find duplication concretely rather than by reading everything:

```bash
# Identifiers named in more than one article are where duplication concentrates.
grep -ohE '`[a-zA-Z_][a-zA-Z0-9_.]{4,}`' docs/kb/*.md | sort | uniq -c | sort -rn | head -30
```

## 2. Deduplicate - one home, everywhere else an edge

For each fact stated in more than one place, pick the article that **owns** the
files it concerns. That is its home. Everywhere else, replace the restatement
with an edge naming the obligation (SPEC.md §4).

Weak, in three articles at once:

> Scores are computed server-side because the client cannot see other
> participants' data.

Better, in the two that do not own it:

> See [arch-scoring.md](arch-scoring.md) - it owns the server-side computation
> invariant this surface depends on.

The edge is *better* than the copy, not just shorter: it cannot drift, and it
tells the reader where the authoritative version lives.

Judgement call: a one-line restatement at the point of use is sometimes worth
its cost, when following the edge would derail the reader mid-procedure. A
paragraph never is.

## 3. Collapse decision logs into rules

A `DECISION` section that reads as history:

> We first tried A. That broke X, so we moved to B in March. B had problem Y,
> so we now do C.

should become the rule plus the reasons the alternatives fail:

> **We do C.** A breaks X (every request re-reads the parent doc). B breaks Y
> (it cannot express a partial period).

Same information, a third the length, and it now answers the question a reader
actually arrives with: *may I change this to A?*

**Keep the dates on retention decisions.** "Kept for older clients" with no
date is a landmine - the next reader cannot tell whether it is load-bearing
today or stale from three releases ago. Compress the narrative, keep the stamp.

## 4. Resize articles

**Split** when two invariant sets have nothing to do with each other, or when
readers reliably need a third of it. The split usually already exists in the
headings.

**Merge** when an invariant is restated in both, or the edge between them says
"and also read the other half of this".

After either, fix `covers:`, `## FILES`, `INDEX.md` and every inbound link.
`kb graph` catches the dangling ones - but it cannot catch an article that
still *exists* and no longer says what its inbound edges promised, so re-read
the edges pointing at anything you split.

## 5. Strengthen edges

The dead-end ratchet enforces that edges *exist*. Nothing enforces that they
are worth following, so this pass is the only thing that ever checks.

Read every `## SEE ALSO` entry and ask: does this name an obligation, or a
topic? Rewrite the topic ones. "Related to X" is not an edge.

## 6. Compress the always-loaded instructions too

If `AGENTS.md` (or a tool-specific equivalent) has grown, it is the most
expensive prose in the repo - it loads on every single turn. Anything there
that has depth in an article should become a pointer to the article.

Keep in the always-loaded file: the rule, the trigger, and where to go.
Move to the article: the reasoning, the history, the worked example.

## 7. Verify

```bash
kb graph && kb fresh
```

Compression edits articles, not code, so `kb fresh` should stay green - the
hashes are over source files. If it goes red, you edited code by accident.

Then read your own diff for the one thing no checker can see: **did any fact
get deleted rather than moved?** Compression is the one pass where a
plausible-looking edit can silently destroy the only record of something. For
each removed paragraph, be able to name where it now lives.

## 8. Report

- articles merged / split, and why
- facts deduplicated, and where each now lives
- edges rewritten from topics into obligations
- total lines before and after

A pass that finds nothing worth compressing is a real result. Say so and stop
rather than manufacturing churn.
