# bench — does the KB actually help an agent find the right file?

```sh
sh bench/run.sh          # fetch, isolate, score. Minutes, no model, no Docker.
```

Everything here is reproducible from two scripts and a public dataset. Nothing
under `cache/`, `work/` or `tasks*.json` is tracked.

## Two tiers, and which question each answers

| | asks | cost |
|---|---|---|
| **Tier 1** `route.py` | which file does a ranking put first | seconds, no model |
| **Tier 2** `agent.py` + `judge.py` | is the work the agent produced better | model calls per run |

Tier 1 is cheap and it is the axis the literature measures. It is also the
weaker question, and on a real KB it turned out to be close to meaningless -
see "What Tier 1 cannot see" below. Tier 2 is the one to trust.

## Tier 2 — does the KB make the work better?

```sh
python3 bench/agent.py --tasks bench/tasks-vv.json --root bench/work-vv --limit 8
python3 bench/judge.py --tasks bench/tasks-vv.json --repo bench/cache/local__vitalsversus
```

**The arms are the repo with and without its knowledge base.** `none` removes
the articles, the CLAUDE.md lines pointing at them, the compiled-gate hook and
the kb-compile skill. Nothing else differs, and that took more care than
expected: across five months of history `docs/kb` is named from 30 different
CLAUDE.md sections, several of which also carry unrelated `MANDATORY` policy.
Deleting those sections would have made `none` the arm with less guidance
rather than the arm with no KB, so the strip is sentence-level and asserts
afterwards that no `docs/kb` survived.

**The judge follows [SWE Atlas](https://arxiv.org/abs/2605.08366)**, which
grades coding agents past issue resolution with rubric-based LLM-as-a-judge.
Adopted because they validated it: atomic self-contained binary items graded
independently, Cohen's kappa 0.88 against human graders with Opus and ~0.98
self-consistency, with negative rubrics alongside positive ones. Their headline
result is why Tier 2 exists at all - frontier models pass 60-80% of regression
tests but only 20-40% of rubric checks. *Passing* and *being good work* are
different measurements and only the first one is cheap.

Rubrics are generated from the bug report and the maintainer's real fix, before
any candidate is seen and without ever reading the KB. Grading is blind: the
judge sees "candidate patch", never an arm name.

**The limitation, which is not small.** The gold patch is one human's fix, not
the only correct one, so a good patch that solves the problem differently loses
items it should not. SWE Atlas filters over-prescriptive rubrics with three
expert reviewers; there is no such panel here. Items are phrased toward
behaviour rather than implementation to blunt it, and small deltas between arms
should be read as noise.

## What Tier 1 cannot see

Run against a five-month-old KB, article-level recall@5 came out:

| | R@5 | |
|---|---|---|
| random | 0.320 | arbitrary fixed article order, ignores the question |
| size | 0.527 | biggest article first, ignores the question |
| paths | 0.577 | lexical match on file paths |
| kb | 0.275 | the KB's own claim text |

Two things are true here and they pull in opposite directions. The KB really
does rank below arbitrary ordering, which is a fact about that KB's format.
But `paths` beating everything is mostly a size prior - an article owning 74
files gets 74 chances to match - and file-level recall is worse still: the
median matched article puts 13 files in one scoring tie, max 158, so R@5 was
largely measuring tie-break order.

The deeper problem is that an agent does not consume a ranked list. A KB that
correctly says "this is the challenge subsystem" and hands over 74 files scores
terribly while behaving exactly as designed. Whether the resulting change is
more careful is invisible to every metric on this page above Tier 2.

## Other evaluators considered

- [SWE Atlas](https://arxiv.org/abs/2605.08366) - the rubric method used here.
  The benchmark itself is fixed repositories, so the dataset does not transfer
  to someone's own repo and their own KB; the method does.
- [ProcBench](https://arxiv.org/html/2605.20251) - process-level defects and
  "control preservation", the closest published framing to *how much oversight
  did this need*. An ontology of process defects rather than a drop-in harness.
- [LLM Critics for execution-free evaluation of code changes](https://arxiv.org/pdf/2501.16655)
  - grading a diff without running it, which is the situation here.

## What Tier 1 measures, and why that and not something else

**Localisation, not resolve rate.** Two 2026 results decide this and they agree:

- [Shepard & Albrecht](https://arxiv.org/abs/2606.20512) tuned repository
  guidance on SWE-bench Verified: 25.5% unguided → 28.3% with a static
  knowledge base → 33.0% refined. The gain "comes from coverage rather than
  precision" — 14.5pp more evaluable patches with per-patch precision flat at
  ~59%. **Guidance helps an agent reach the right file. It does not help it
  write a better change once there.**
- [Gloaguen et al.](https://arxiv.org/abs/2602.11988) measured task success
  directly and found context files do not generally improve it, at >20% more
  inference cost, for both LLM-generated and developer-committed files.

So resolve rate is the metric least likely to move and most expensive to
collect — a container and a test suite per instance. Recall@k on the gold
patch's file set is the axis the effect lives on, and the ground truth is free.

**The metric's honest ceiling:** the gold set is what the patch *edited*, which
under-counts files you had to *read* to know what to edit.
[ContextBench](https://arxiv.org/abs/2602.05892) pays annotators for the fuller
set across 1,136 tasks; this is the free approximation, and a floor rather than
the whole question.

**The `paths` arm is the control.** Lexical match between the issue and file
paths — what a directory tree gives you for nothing. A KB that does not beat it
is filing rather than describing, and no amount of delivery fixes that.

## The isolation, which is the point

`git clone` brings every ref. Checking out a parent detaches HEAD and leaves
`origin/main` pointing at the future in the same object store, one flag away.
An earlier version of this experiment did exactly that: sixteen agents were
asked to diagnose a bug whose fix was reachable in their own clone. Two read the
fix commit and reported its reasoning back as their diagnosis, one quoting the
commit message's figures. The runs agreed with each other, which read as
corroboration and was shared contamination.

`prepare.py` therefore has two modes and **verifies the negative in both**:

| mode | how | history available |
|---|---|---|
| `archive` (default) | `git archive` the tree at `base_commit`; no `.git` exists | none — contamination impossible by construction |
| `history` | clone, reset to `base_commit`, drop the remote, delete every other ref, expire reflog, `gc --prune=now` | ancestors only |

Both record the pre-strip tip commit and then assert it is **unresolvable**. A
task whose isolation cannot be proven is refused, not run. There is a second,
independent check that fails differently: the gold files must exist in the tree,
which catches a post-fix checkout even if the ref logic were wrong.

`history` mode exists because `kb-catchup` legitimately reads git history to
harvest deleted prose. Prefer `archive` unless the run needs that.

## Tier 2 — agents, when you want the real number

Not built. The design, so it is not re-derived:

- Arms are **instructions vs overview**, not KB vs no-KB. Gloaguen's result is
  that *"instructions in the context files are well followed by coding agents,
  repository overviews, although popular and recommended by model providers,
  are not helpful."* In kb-compile's vocabulary `INVARIANT` and `GOTCHA` are
  instructions; `DECISION` and the whole of `DIGEST.md` are an overview. So:
  `none` / `kb owners` (instructions only) / `DIGEST.md` (overview). That is
  the live disagreement, stated in terms this tool can act on.
- Score localisation from the agent's stated edit set, not from a patch.
- **n matters.** The literature's effect is ~3pp between unguided and a static
  KB. Nothing below a few hundred instances can see that, and a 5-per-arm run
  on one bug — which is what produced the retracted result above — cannot.

## Files

| | |
|---|---|
| `fetch.py` | SWE-bench Verified → `{problem_statement, gold_files, base_commit}`. Caps per repo, because a KB costs one generation per repo and breadth is what the sample needs. |
| `prepare.py` | Materialise + isolate + prove the isolation. |
| `route.py` | Tier 1 scorer. Paired comparison only — the arms run on different task counts whenever some repos carry a KB, and an unpaired mean compares the task sets rather than the arms. |
