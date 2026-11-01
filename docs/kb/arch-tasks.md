---
id: arch-tasks
type: arch
covers:
  - internal/kb/task.go
see_also:
  - arch-gates
  - ref-surface
---
# Task notes

`kb task` keeps an agent's working notes in `.kb/tasks/<id>/`, ignored by git,
so it can drop its raw trajectory and still find its place after compaction.
SPEC.md §5 says where this sits relative to the generated artifacts.

## FILES

| Path | Role |
|---|---|
| `internal/kb/task.go` | The task directory, the log format and the resume view |

## INVARIANT

**A task log never writes an article and never runs `kb compiled`.** A log is
unverified, and a green `kb fresh` means somebody reviewed the articles. If a log
could become KB content without review, that green would stop meaning it.
`runTask` is dispatched before the checker exists, so a task command has no
ownership map to touch a gate with. `TestTaskLifecycleLeavesTheKBAlone` requires
`docs/kb` to stay byte-identical and a stale `kb fresh` to stay red.

**The default `kb task context` stays within `ContextBudget`.** It is read right
after compaction, so output that grew with the log would spend the room compaction
made. Every growing input has its own limit and a final clip enforces the total;
`TestTaskContextStaysBounded` grows all of them at once, because a test of only
the log passes with an unbounded file list.

**`kb task drop` only accepts a single path segment.** It ends in `RemoveAll`.

## DECISION

**`start` writes `.kb/tasks/.gitignore` itself, rather than relying on the repo's
ignore file.** An adopting repo has no entry for the directory, so `git add -A`
would commit raw tool output.

**One CURRENT per checkout, and a second `start` is refused.** Overwriting would
send the first session's milestones into the second's log, with neither told. A
per-session pointer was not built because worktrees already give parallel work its
own directory.

**Touched files come from first-parent, non-merge commits, not `git diff
start..HEAD`.** The diff reports trunk's files after a merge-down, which sends
harvest to articles the task never affected. A rebase cannot be seen through this
way, so the output says the list may overstate.

**Milestones are split on a marker line, not a heading.** Bodies are markdown an
agent wrote. `TestTaskBodyCannotSplitAMilestone` includes a body quoting the marker.

**The stdin wait covers only the first byte.** An agent's shell can hand down a
pipe nobody writes to or closes, and hanging would stop the agent at the command
it runs to save its place. Once a byte arrives it reads to EOF, so a slow writer
loses nothing.

## GOTCHA

**`hooks/kb-fresh-gate.sh` matches the substring `git commit`, so `kb task commit
"git commit failed"` is denied while the KB is stale.** Left alone deliberately:
exempting `kb task` would also pass `kb task commit x && git commit`, and a false
denial beats a gate that fails open. Reword the message.

## SEE ALSO

- [arch-gates.md](arch-gates.md) - owns the freshness gate these notes must never
  feed, including the commit hook that misreads a task message.
- [ref-surface.md](ref-surface.md) - owns the command dispatch `kb task` is routed
  through ahead of the checks.
