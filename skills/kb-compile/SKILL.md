---
name: kb-compile
description: Reconcile the knowledge base with code that has changed, then record the review. Uses `kb fresh` output as the exact work list, re-reads each named article against its changed files, fixes what is now false, and runs `kb compiled`. Run before committing any code change.
allowed-tools: Bash, Read, Write, Edit, Glob, Grep
---

Bring the KB back into agreement with the code, then record that you did.

**Cost:** scoped to what changed, so an ordinary pass is small (one to a few
articles). It only gets expensive after a long gap or a wide refactor, and
`kb fresh` tells you which before you begin.

Read [`SPEC.md`](../../SPEC.md) if you have not - especially §5 on why the
manifest records a judgement rather than a derivation. That is the whole reason
this skill is a skill and not a script.

---

## 1. Get the scope

```bash
kb fresh
```

This is the work list. It names every KB-owned file whose content differs from
what was last reviewed, **grouped by the article that owns it**.

Use it rather than a commit range. It is strictly more precise: a file touched
and then reverted since the last compile does not appear, and a file changed
across five commits appears once. It also survives merges - bringing trunk down
into a branch does not create work for files already reviewed on the other
side.

Then look for finished task notes:

```bash
kb task list
```

**A finished task can be waiting even when `kb fresh` prints OK.** The commit gate
compiles work as it lands, so a task whose commits are all in has a green gate and
still has lessons in its log. Step 6 covers it.

If `kb fresh` prints OK and no finished task is listed, there is nothing to compile.
Say so and stop.

## 2. Reconcile each article

For each article the output names, read the article and read the changed files
it owns. If the article is large and the change is narrow, read one section
rather than all of it:

```bash
kb read arch-scoring#INVARIANT
```

You are looking for four specific things - not "does this feel current":

1. **Statements that are now false.** A named function that was renamed, a
   count that moved, an invariant the change deliberately broke. This is the
   dangerous category: an agent is told to read the article first, so a
   confidently-wrong sentence gets acted on instead of the code.
2. **Decisions the change made that are not recorded.** If the diff chose
   between two approaches, the rejected one belongs in `DECISION`. This is the
   knowledge that is genuinely unrecoverable later - six months on, nobody can
   reconstruct why from the code, and the article is the only place it could
   have lived.
3. **New non-derivable context.** A new gotcha, a new cross-file obligation, a
   new "these two must change together".
4. **Paths that moved.** `kb graph` catches dangling paths, but it cannot tell
   you that a path is still valid and now describes something else.
5. **Dated claims about anything outside the repo.** No file change will ever
   flag these, so this pass is the only time anyone looks. If the change you
   are compiling touched the thing an old dated assertion describes, recheck
   it and restamp it - or delete it, if checking is not worth the effort.
   An undated external claim is one to date now. SPEC.md §2, "Claims the
   freshness gate cannot check".

Then fix the article. Editing prose is the job; the checker only ever tells you
where to look.

**Do not add description.** A change that only moved code around usually needs
no article edit at all, and "no change needed" is a legitimate and common
outcome. Adding a paragraph so the compile feels productive is how a KB gets
fat and stops being read.

**Never shorten a reason into a summary of itself.** Rewriting is how a stated
reason quietly becomes a restated fact: "kept off the tunnel because bulk video
violates TOS 2.8 and no client app can complete the Access redirect" is two
reasons, and compressing it to "kept off the tunnel for TOS reasons" loses the
half nobody can re-derive. If a paragraph is genuinely too long, the fix is to
cut the description around the reason, not the reason.

If the change was informed by something written down outside the article - a
commit message that argued a case, a README, an incident writeup - read that
before editing, and carry the reasoning across rather than paraphrasing the
diff. See kb-catchup step 3.

## 3. Handle files with no owning article

If `kb fresh` lists files under "(no owning article)", or `kb graph` fails the
orphan ratchet, a new production file needs claiming: add it to a `## FILES`
block, or widen the owning article's `covers:` glob.

Widening a glob claims the file. Only do it if the article genuinely covers
what the file does - otherwise write it into the right article, or a new one.

## 4. Check the structure

```bash
kb graph
```

Catches what prose review cannot: dangling links, dead globs, a new dead end,
an article missing from `INDEX.md`, a stale reverse index.

Every message names the fix. Follow it.

## 5. Fill gaps you notice

A compile scoped to this change is the minimum, not the ceiling. If you are in
an article and spot something wrong that has nothing to do with today's diff -
a stale signature, a decision recorded backwards, a section describing a
subsystem that was split last quarter - **fix it now**.

You are the only reader that article is guaranteed to get before the next agent
acts on it. The scoping exists to stop the compile being unbounded, not to stop
you fixing what is in front of you.

## 6. Pull in finished tasks

For each task `kb task list` shows as `complete`, read `.kb/tasks/<id>/HARVEST.md`.
If it has none, follow the [kb-harvest](../kb-harvest/SKILL.md) skill to write one
first.

**A harvest is evidence, not authority.** It came from an unverified log. For each
delta, check it against the current code and against the article, then accept it,
reword it, or reject it. Reject anything derivable from the code, anything
the code now contradicts, and anything a later commit overtook. A claim about why
something was rejected often cannot be checked against code. Keep it only if it is
specific and dated, and do not present it as more certain than the log was.

This applies to the articles `kb fresh` names and to any other article a harvest
names. Leave a task marked `current` alone: it is still in progress, and it may
belong to another session.

When you have been through a task's deltas, delete its notes. The review is
finished, and whatever mattered is in an article now:

```bash
kb task drop <id>
```

If you edited an article here, run `kb graph --write` again: it catches a bad path
in a delta and regenerates the digest.

If you did not finish a harvest, leave that task and say so in your report.

## 7. Record the review

Harvested edits change articles, not source, and the manifest only hashes source.
So if `kb fresh` printed OK in step 1, there is nothing to record: `kb compiled` has
no new content to attest, and you skip to the report.

Otherwise, run what `kb fresh` told you to run. On a current build that is a scoped
command naming every article it listed, and you delete the ones you did not get
to:

```bash
kb compiled arch-scoring arch-ingest
```

Take the shape from the output rather than from here. Builds before the scoped
form end with "record it with `kb compiled`" and silently discard article
names, so on those, deleting names records everything anyway and the honest
edit produces the dishonest result. The line `kb fresh` prints always comes
from the binary that will consume it; this file does not.

If your `kb fresh` printed no command, re-read every article it named before
recording, because bare `kb compiled` attests to all of them.

**Only record what you actually read.**

This writes down "these articles were checked against this content", and
nothing downstream can tell the difference between a careful pass and a bare
`--write`. There is no verification behind this claim; it is load-bearing on
your honesty, and every future agent inherits the consequence.

Bare `kb compiled` records every KB-owned file at once, which asserts that
every article the gate named was reviewed. Use it only when that is true. It
prints how many articles it just spoke for, so check that number against the
ones you opened.

If you ran out of context, or could not tell whether a statement was still
true, record the articles you did finish and say which you left. A gate that
stays red on two articles is worth far more than a green one that lies about
five, and far more than silence - which is also what a careless pass produces.

The claim is only as fine as the widest glob you name. An article covering a
directory is attested whole, so if it was flagged for five files and you read
two, either read the other three or leave it red.

Escape hatch for a revert or a pure rename, where content moved but nothing to
review changed:

```bash
KB_COMPILE_OK=1 kb fresh
```

Typing that often means the check is wrong and should be fixed, not silenced.

## 8. Report

Say what you checked and what you changed:

- articles reviewed, and which needed edits
- tasks harvested and dropped, with the deltas you accepted and rejected
- anything you fixed that was outside today's change
- anything you could not verify

"Reviewed 3 articles, 1 needed an edit, 2 were already accurate" is a complete
and useful report. A clean result is a real result.
