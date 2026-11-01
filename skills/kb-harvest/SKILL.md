---
name: kb-harvest
description: Turn a finished task's notes into proposed KB changes. Reads the task log and diff, decides whether anything durable came out of it, and writes itemized deltas to HARVEST.md for kb-compile to accept or reject. Never edits an article and never runs `kb compiled`. Run after `kb task finish`, when ending an agent. "Nothing durable" is the expected result.
argument-hint: [task-id]
allowed-tools: Bash, Read, Write, Glob, Grep
---

Propose what a finished task should add to the KB. Propose only: a task log is
unverified scratch, and it reaches an article through kb-compile's review or not
at all.

**Cost:** small, usually a few thousand tokens for the log and the diff, plus one
section per affected article. A long task with a wide diff costs more. Use
`git diff --stat` first and read the hunks the log points at.

Read [`SPEC.md`](../../SPEC.md) if you have not, especially §5 on judgements and
derivations. Do not edit an article here and do not run `kb compiled`. Either one
lets a log become KB content with nobody having reviewed it, and then a green
`kb fresh` stops meaning anything.

---

## 1. Read the task

```bash
kb task list
kb task context --full <id>
git diff <start-HEAD>..HEAD
```

The start HEAD is on the second line of the context output, and `--full` lists the
files the task touched. The id is required once the task is finished, because
`kb task finish` clears CURRENT. If the diff is huge or the start HEAD no longer
resolves after a rebase, go by the touched-file list and read those files.

## 2. Find the affected articles

```bash
kb owners <touched files>
```

## 3. Decide what is durable

For each affected article, ask whether the task produced something that outlives
it:

- an invariant learned: name what breaks if it stops being true
- a decision, with the alternative it rejected
- a failure mode that was actually hit

Most tasks produce nothing beyond what the code already says. **"Nothing durable"
is a valid and expected outcome, so say it plainly when it is true.** Write a
HARVEST.md that says so, and do not invent a delta to look productive.

**The log's real value is the why: what was tried and rejected.** The diff already
carries the what, and an article restating it is negative value (SPEC.md §1). So
harvested content usually belongs in `DECISION`, and the log is often the only
place the rejected alternative was ever written down.

Leave out anything that was only true during the task: dead ends explained by a
typo, the order you did things in, what you plan next.

## 4. Write the proposals

Write `.kb/tasks/<id>/HARVEST.md`. Itemized deltas, never a rewritten article:

```markdown
# Harvest for <id>

## arch-scoring

- **add** to `DECISION`: Windows are computed per tenant, not globally. A global
  cache was tried on 2026-10-30 and served one tenant's boundaries to another.
  From: log.md, milestone 2026-10-30T14:02:11Z "dropped the global cache"

- **amend** in `INVARIANT`: "<the current claim>" becomes "<the new claim>".
  From: `app/scoring/windows.py` hunk `@@ -40,7 +40,9 @@`

- **remove** from `GOTCHA`: "<the claim>". The retry it warns about is gone.
  From: `app/scoring/retry.py` deleted in this diff
```

Each delta names its article, its section, and the log line or diff hunk it came
from. kb-compile has to be able to check the source without redoing your reading.

Write each delta as it should read in the article. The log gets deleted, so:

- Carry the reason and an absolute date into the text (SPEC.md §2).
- Do not cite `.kb/tasks/...` in the proposed text. Git does not track it, so
  `kb graph` fails the article, and the file is gone after the drop anyway.
- Do not soften a claim the log only guessed at into a fact. Say it was unverified,
  or leave it out.

## 5. Hand off

Tell the user to run kb-compile, or run it yourself if you are ending this agent
and that is what you were asked to do. kb-compile accepts or rejects each delta in
its normal review, then records anything that needs recording. That review is what
makes these claims KB content, so a delta nobody reviewed has not landed.

## 6. Clean up

Once kb-compile has been through the harvest:

```bash
kb task drop <id>
```

This deletes the proposals too, so do not run it before the review has happened.
If the harvest said nothing durable, there is nothing to review and you can drop
the task straight away.
