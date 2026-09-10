---
name: kb-catchup
description: Back-fill a knowledge base until the orphan baseline shrinks to zero. Clusters undocumented production files into subsystems, writes one article per subsystem, and ratchets the baseline down in verified batches. Use after kb-init, or any time the orphan baseline is large and needs working through.
argument-hint: [subsystem-or-path]
allowed-tools: Bash, Read, Write, Edit, Glob, Grep, Agent
---

Work the orphan baseline down to zero, one subsystem at a time.

Read [`SPEC.md`](../../SPEC.md) first, particularly §1 (what belongs in an
article) and §7 (sizing). This skill is mostly about *not* writing the wrong
thing at scale.

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

**Do not go file by file, and do not go directory by directory.** A subsystem
is a set of files that *change together and share invariants*. Directory
structure is a filing decision and the boundaries frequently cut across it.

Cluster with evidence, not vibes:

```bash
# What actually changes together - the strongest signal available.
git log --format='%H' --since='18 months ago' -- <root>/ |
  while read c; do git show --name-only --format= "$c"; echo '---'; done

# Who imports whom.
grep -rn "^import\|^from\|require(" <root>/ | head -50
```

Co-change is the signal to trust. Two files that always appear in the same
commit belong in one article whichever directories they live in; two files in
the same directory that have never changed together probably do not.

Aim for **5–20 files per article**. One file per article means the KB is a
second copy of the tree. Fifty means nobody reads it.

Write the cluster list down and show it before writing anything. A wrong
clustering is expensive to undo later - articles get linked, cited and gated
against - and cheap to fix now.

## 3. Write one article per cluster

For each cluster, before writing a word:

1. **Read the code.** All of it, not the entry point.
2. **Read its history.** `git log -p --follow` on the two or three most-churned
   files. This is where decisions and gotchas actually live.
3. **Find the bugs.** Search issues/commits for the subsystem's names. A fixed
   bug is a `GOTCHA` with evidence attached.
4. **Read the tests.** A test asserting something strange is usually an
   invariant nobody wrote down.

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

Claim the files with a `covers:` glob when the cluster is a clean subtree, or
an explicit `## FILES` block when it is not. Prefer the glob - it keeps
covering files added later, which is exactly the point of a ratchet.

Add the article to `INDEX.md` in the same edit. An unlinked article fails.

### Parallelising

Clusters are independent, so this fans out well: one agent per cluster, each
producing one article. Two rules if you do:

- Give each agent the cluster's file list and the SPEC, and require it to cite
  evidence per claim. An agent with a vague brief writes the fluent prose.
- Do the `SEE ALSO` edges **after** the fan-in, centrally. Agents cannot write
  edges to articles that do not exist yet, and each guessing at the others
  produces dangling links and duplicated framing.

## 4. Ratchet down, in verified batches

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

## 5. Finish

When the baseline reaches zero, that is a real milestone: every production file
now has an article that must be kept accurate.

```bash
kb graph && kb fresh
```

`kb fresh` will now name every file you just claimed - they are new to the
manifest. That is correct and it is the moment to be honest: you reviewed them
while writing the article, so recording it is legitimate.

```bash
kb compiled
```

If you claimed files with a glob **without** reading them, say so and go read
them. Recording a compile you did not do is the one action that breaks the gate
permanently, silently, and for everyone.

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

**Directory-shaped articles.** One article per directory produces a KB that
mirrors the tree and adds nothing. Cluster by co-change.

**Glob creep.** `covers: src/**` on one article silently owns everything and
drives the orphan count to zero while documenting almost none of it. If a glob
claims files the article never mentions, it is too wide.

**Evidence-free gotchas.** "Be careful with concurrency here" is not knowledge.
Either find the bug it refers to or drop it.

**Writing the article the code would write.** Re-read §1 of the SPEC. This is
the one that ruins the whole exercise, and it ruins it invisibly.
