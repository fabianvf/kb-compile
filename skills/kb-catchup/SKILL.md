---
name: kb-catchup
description: Back-fill a knowledge base until the orphan baseline shrinks to zero. Clusters undocumented production files into subsystems, writes one article per subsystem, and ratchets the baseline down in verified batches. Use after kb-init, or any time the orphan baseline is large and needs working through. EXPENSIVE, roughly 30-80k tokens per article written; tell the user the estimate and get agreement before starting a full run.
argument-hint: [subsystem-or-path]
allowed-tools: Bash, Read, Write, Edit, Glob, Grep, Agent
---

Work the orphan baseline down to zero, one subsystem at a time.

Read [`SPEC.md`](../../SPEC.md) first, particularly §1 (what belongs in an
article) and §7 (sizing). This skill is mostly about *not* writing the wrong
thing at scale.

---

## Cost, before you start

This is the expensive skill in the set, and the cost scales with the worklist
rather than with the request. Say the number out loud before starting.

Writing one article properly means reading a subsystem's code, its history and
its tests. That runs roughly **30-80k tokens per article**, more for a gnarly
subsystem. Clusters land around 5-20 files, so:

| Orphans in the baseline | Articles, roughly | Rough token cost |
|---|---|---|
| 50 | 5-8 | 200-600k |
| 150 | 12-20 | 500k-1.5M |
| 500 | 35-60 | 1.5-4M |
| 1000+ | 70+ | 3M+ |

Two things follow.

**Get agreement on scope first.** Report the orphan count and the estimate,
and offer to do a batch rather than the whole thing. Nobody wants to discover
the size of this after it has started.

**Batch regardless.** Work in groups of 3-5 articles, commit each group, and
stop cleanly at a boundary. The ratchet is designed for partial progress, and a
run that dies mid-way having committed nothing has spent the tokens for no
result.

If you are asked to just make the number go down cheaply, say no. Wide `covers:`
globs would do it in minutes and produce a KB that claims coverage it does not
have, which is worse than the orphans.

---

## The thing that will go wrong

You are about to write many articles from a worklist. The failure mode is not
running out of steam - it is producing fluent architectural prose that
describes what the code does.

That output looks excellent. It passes every check. It helps nobody, and it
costs a re-read every time the code moves, forever. A KB of it is worse than no
KB, because agents are told to read it first and it displaces reading the code.

**Every claim in an article must be non-derivable and evidenced.** If a
competent agent could have gotten it by reading the file, delete it. If you
cannot cite where you learned it - a commit, a bug, a test, a comment, a code
path that only makes sense one way - you do not know it, and writing it down
anyway manufactures a confident falsehood that someone will act on.

An honest short article beats a plausible long one. "This module does X; the
only non-obvious thing is Y" is a *fine* article.

---

## 1. Get the worklist

```bash
grep -v '^#' docs/kb/.orphans-baseline.txt | grep -v '^$'
```

That is the exact set of production files with no KB home, derived from
`covers:` globs and `## FILES` claims rather than from anyone's memory.

## 2. Cluster into subsystems

A subsystem is a set of files that **change together and share invariants**.
The goal is one article per subsystem.

Roughly **5 to 20 files** is a smell test, not a target: one file per article
makes the KB a second copy of the tree, and fifty means nobody reads it. Where
the invariants genuinely divide at two files, two files is the right article -
SPEC §7 governs, and an adopter who padded to hit a count would be writing
description. Both first-time runs produced several articles under five files
and both were right to.

**Start from the directory partition, then correct it with co-change.** Do not
try to derive clusters from co-change alone. It looks principled and it does
not work: co-change is a sparse graph, and any transitive grouping over it
collapses into one giant component plus a long tail of singletons. Measured on
a 509-commit repo with 130 orphans, union-find over pairs co-changing 3+ times
gave exactly 1 cluster of 8 and 122 singletons, while the directory partition
gave 42 usable groups.

So:

```bash
# 1. The starting partition. Directories are a real signal, especially in
#    package-based languages where a directory IS the unit of import.
grep -v '^#' docs/kb/.orphans-baseline.txt | grep -v '^$' |
  sed 's|/[^/]*$||' | sort | uniq -c | sort -rn
```

Then use co-change for the two corrections it is genuinely good at, which is
telling you where the directory partition is *wrong*:

```bash
# 2. MERGE candidates: pairs in DIFFERENT directories that keep moving
#    together. These are the boundaries the tree gets wrong.
git log --format='%H' --name-only --no-merges |
  awk '/^[0-9a-f]{40}$/{print "---"; next} NF{print}' |
  python3 -c '
import sys, itertools, collections
groups, cur = [], []
for l in sys.stdin:
    l = l.strip()
    if l == "---":
        groups.append(cur); cur = []
    elif l: cur.append(l)
pair = collections.Counter()
for g in groups:
    if 1 < len(g) <= 12:
        for a, b in itertools.combinations(sorted(set(g)), 2):
            if a.rsplit("/", 1)[0] != b.rsplit("/", 1)[0]:
                pair[(a, b)] += 1
for (a, b), n in pair.most_common(20):
    print(n, a, b)'
```

A pair near the top of that list, in two different directories, is a merge
candidate: put both in one article.

For the opposite correction, look at a large directory and ask whether its
files ever appear in the same commit. A directory whose files never co-change
is usually two subsystems sharing a folder, and it should split.

Neither correction is automatic. The commands produce candidates; you decide,
because the question is whether they share INVARIANTS, and only reading them
answers that.

Write the cluster list down and show it before writing anything. A wrong
clustering is expensive to undo later, since articles get linked, cited and
gated against, and it is cheap to fix now.

## 3. Write one article per cluster

For each cluster, before writing a word:

1. **Harvest the prose that already exists.** Do this FIRST, not last. A
   repository that has been maintained by people who write things down already
   holds reasoning nobody can re-derive, and it is sitting outside the files
   you are about to read - so the code is in front of you and the explanation
   is not.

   ```bash
   # Prose anywhere in the repo that already discusses this subsystem
   git ls-files '*.md' '*.rst' '*.adoc' | xargs grep -lni 'cloudflared\|tunnel'
   ```

   Look at README files at every level, a `docs/` or `knowledge-base/` tree,
   ADRs, incident writeups, the repo's own `AGENTS.md` or `CLAUDE.md`, and
   long comment headers in files ADJACENT to the ones you are covering.

   **Look in git history too.** A previous adoption attempt, or a doc deleted
   in a refactor, is invisible to the command above and is often the single
   richest source in the repo:

   ```bash
   git log --diff-filter=D --name-only --oneline -- '*.md' | head -40
   git show <commit>^:path/to/deleted.md
   ```

   **Confirm every harvest target with `git ls-files` before citing it.** A
   repo's own index rots exactly like the KB it is meant to feed: one adopter
   found a `knowledge-base/README.md` listing twelve documents that no longer
   existed, and `kb graph` fails any article that backticks one.

2. **Read the code.** All of it, not the entry point. In a commented
   language, pulling every module and function docstring in one pass is far
   cheaper than opening files one at a time and gets most of the
   non-derivable content at once - one adopter extracted 3,800 lines from 61
   Python files that way. Then read the files that look interesting.
3. **Read its history.** `git log -p --follow` on the two or three most-churned
   files. This is where decisions and gotchas actually live.
4. **Find the bugs.** Search issues/commits for the subsystem's names. A fixed
   bug is a `GOTCHA` with evidence attached.
5. **Read the tests.** A test asserting something strange is usually an
   invariant nobody wrote down.

This does not contradict SPEC's "if a competent agent could have derived it by
reading the file, delete it". That rule is about the reader's whole task, not
one file in isolation — see SPEC.md §2, "Derivable from the file, or derivable
from ONE file?". A comment speaks only for its own file; lifting its reason
into the article that owns the cross-file obligation is the job, and restating
it verbatim to fill a section is the failure.

**Chew on what you harvest; do not skim it.** A reason written in prose is
usually there because somebody hit the thing, and the ones worth most are the
ones no amount of reading the code would produce. This actually happened while
bootstrapping a homelab repo: an article was written from
`check-exposure.py`'s docstring and said Jellyfin is kept off the Cloudflare
tunnel because bulk video violates TOS 2.8. The existing `exposure-policy.md`
gave a second reason - **no Jellyfin client app can complete the Access
browser redirect** - which is not inferable from any file in the repo, and the
new article silently dropped it.

**An article that restates existing prose more thinly is a REGRESSION, not a
migration.** It is worse than leaving the prose alone, because the article is
the one the reverse index points at and the one agents are told to read first,
so the better text is displaced by the weaker one while looking authoritative.
Before finishing an article that covers ground existing prose already covers,
diff them in your head: every reason the prose gives and the article does not
is a reason you are about to lose.

Taking the existing text nearly verbatim is the CORRECT outcome when it is
already good. The value you add is the frontmatter, the file ownership, the
section structure and the checked paths - not rewording someone's hard-won
explanation.

Then write, using the sections from SPEC.md §2. The bar for each:

- **`INVARIANT`** - name what BREAKS if it stops being true. "Scores are
  computed server-side" is a description. "Scores are computed server-side
  because the client cannot see other participants' data; a client-side
  recompute silently drifts for anyone in a different timezone" is an
  invariant.
- **`GOTCHA`** - cite the incident. No incident, no gotcha; call it a note.
- **`DECISION`** - name the rejected alternative. Without one it is a fact
  about the current code, which the code states better.
- **`SEE ALSO`** - at least one, naming an *obligation* (SPEC.md §4). Required:
  an article with no outbound edge fails the dead-end ratchet.
- **Any claim about the world outside the repo** - a vendor's terms, a client
  app's behaviour, what is actually deployed - carries the date you checked it.
  `kb fresh` hashes source files, so it is structurally blind to these and they
  rot looking exactly as authoritative as the checked kind. SPEC.md §2, "Claims
  the freshness gate cannot check". Dates absolute, never "recently".

Claim the files with a `covers:` glob when the cluster is a clean subtree, or
an explicit `## FILES` block when it is not. Prefer the glob - it keeps
covering files added later, which is exactly the point of a ratchet.

Add the article to `INDEX.md` in the same edit. An unlinked article fails.

### Parallelising

**`INDEX.md` is as central as the edges.** Step 3 says to add each article to
it in the same edit, which is right for one author and a guaranteed clobber
for twenty concurrent ones. When fanning out, have the agents skip both
`INDEX.md` and `SEE ALSO`, and write both centrally after the fan-in.

Clusters are independent, so this fans out well: one agent per cluster, each
producing one article. Two rules if you do:

- Give each agent the cluster's file list and the SPEC, and require it to cite
  evidence per claim. An agent with a vague brief writes the fluent prose.
- Do the `SEE ALSO` edges **after** the fan-in, centrally. Agents cannot write
  edges to articles that do not exist yet, and each guessing at the others
  produces dangling links and duplicated framing.

## 4. Ratchet down, in verified batches

A batch must be **edge-closed**: every article a batch's `see_also` names has
to exist by the time you verify, or `kb graph` cannot go green and the batch
cannot close. Size the batch to the cluster of articles that reference each
other rather than to a fixed 3-5, and defer only RECIPROCAL edges - the
back-links - to the end. Both first-time runs hit this on their first batch.

After each batch of articles:

```bash
kb graph --write   # refuses to grow the baseline; only shrinks it
kb graph           # verify green
```

Then read the diff on `.orphans-baseline.txt` and confirm the files that left
are the ones you meant to claim. A `covers:` glob is easy to write one
directory too wide, and the failure is silent: the article now owns files it
says nothing about, so the ratchet stops asking anyone to document them.

Commit each batch. A single commit claiming 300 files is unreviewable, and this
is precisely the work where review is the only quality control that exists.

## 5. Check the carve

```bash
kb eval
```

This asks whether the files that change together share an article, and scores
it against a directory baseline.

**Read the three rows first.** `kb eval` reports recall at the article
boundary, recall after following one `## SEE ALSO` hop, and the directory
baseline - with the reading cost of each. The hop row is the one to look at:
it is how the artifact is actually used, and it is the only row comparable to
the tree, because reading five files is not the same question as reading
twenty-two.

That gives you a cheaper lever than merging. If one hop buys a lot, the carve
is fine and the edges are working; leave both alone. If one hop buys almost
nothing, **fix the edges before touching the boundaries** - an edge is one
`see_also` line and a sentence, a merge throws away an article somebody wrote.

**Then read the worst-scoring changes.** They are scored AT ONE HOP: what a
reader still misses after following the edges they were given. A low score has
two quite different causes and only one of them is a carve problem:

- **Boundaries cut across a subsystem.** The worst changes are small, touch
  two or three articles, and keep touching the same two or three. Merge those.
- **The repo makes sweeping commits.** The worst changes are renames, "tag
  every service", "apply the review", dependency bumps — ten files across six
  subsystems. **No boundary fixes those**, short of one article owning the
  repo, which this tool's own output calls out as scoring 1.00 and routing
  nobody. Do not merge. Say so in your report, with the commit subjects, so
  the number is not read as a defect later.

Two independent first-time runs both hit the second case, both correctly
declined to merge, and both had to argue with this step to do it. If the top
changes are repo-wide sweeps, a KB that loses on recall while reading a third
as many files is working as intended.

Run it after a batch, not after every article. It reads git history, so it
needs enough articles to say anything.

## 6. Finish

When the baseline reaches zero, that is a real milestone: every production file
now has an article that must be kept accurate.

```bash
kb graph && kb fresh
```

`kb fresh` will now name every file you just claimed - they are new to the
manifest. That is correct and it is the moment to be honest: you reviewed them
while writing the article, so recording it is legitimate.

Run what `kb fresh` just printed. On a current build that is a scoped command
naming the articles you wrote:

```bash
kb compiled arch-scoring arch-ingest   # whatever kb fresh named
```

Take the shape from the output, not from here. Builds before the scoped form
end with "record it with `kb compiled`" and discard article names, so naming
one records them all - and if that is your binary, re-read every article
`kb fresh` listed before recording.

If you claimed files with a glob **without** reading them, say so and go read
them. Recording a compile you did not do is the one action that breaks the gate
permanently, silently, and for everyone.

**Do this per article as you finish it, not only here.** Left to the end it
competes with the urge to sign off, and by then the prose you would compare
against is no longer open. Naming the article is what makes that possible:
each one is recorded on its own and the rest stay red.

**Then check you did not make anything worse.** For every article that covers
ground the repo's existing prose already covered, open the prose one more time
and ask what reasons it gives that the article does not. A KB whose articles
are thinner than the docs they now shadow has made the repository harder to
work in while reporting a green gate and an empty orphan baseline - the exact
appearance of success. Report any doc you are leaving in place alongside an
article, and why both should exist.

---

## Partial runs

Zero in one pass is unlikely on a large repo, and stopping is fine - that is
what a ratchet is for. Stop at a batch boundary, with green gates and the
baseline committed. Report the number remaining and which clusters are next.

What is *not* fine is leaving the baseline regenerated but the articles
unwritten, which claims coverage that does not exist. `kb graph --write`
refuses to grow the list; it will happily shrink it because a glob got wide.

---

## Failure modes

**Taking the directory partition as final.** It is the starting point, not the
answer. If no article ended up merging two directories or splitting one, the
co-change pass was skipped and the KB is just a second copy of the tree.

**Glob creep.** `covers: src/**` on one article silently owns everything and
drives the orphan count to zero while documenting almost none of it. If a glob
claims files the article never mentions, it is too wide.

**Evidence-free gotchas.** "Be careful with concurrency here" is not knowledge.
Either find the bug it refers to or drop it.

**Writing the article the code would write.** Re-read §1 of the SPEC. This is
the one that ruins the whole exercise, and it ruins it invisibly.
