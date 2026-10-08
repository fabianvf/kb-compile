#!/usr/bin/env python3
"""Tier 2: run real agents with and without the KB, and keep what they PRODUCED.

WHY THIS AND NOT RECALL@K
-------------------------
The Tier 1 scorer in route.py asks which file a ranking puts first. That is the
axis the literature measures and it is the cheap one, but it answers a question
nobody has: an agent does not consume a ranked list, and a KB that correctly
says "this is the challenge subsystem" and hands over 74 files scores terribly
while behaving exactly as intended.

What a knowledge base is FOR is the work that comes out the other end - a fix
that respects an invariant nobody wrote down, that updates the second call site,
that does not undo a decision made for a reason the code does not state. None of
that is visible in a file ranking. So this runs the agent and diffs the tree.

Cost is not a tiebreaker here. A run that takes twice the tokens and needs no
correction afterwards is the better run.

THE ARMS
--------
    none    the repository with its knowledge base removed - articles, the
            CLAUDE.md section pointing at them, the compiled-gate hook, and
            the kb-compile skill. Everything else byte-identical.
    kb      the repository as it actually ships.
    kb-mig  the same articles rewritten into the SPEC.md vocabulary by
            migrate.py. Same ids, same `covers:` globs, same files owned -
            only the form of the claims differs, which is the whole point.
            Each tree gets ITS OWN articles migrated, never today's, because
            the tasks span five months and today's KB describes fixes that
            had not been made yet.

Anything that differs between the arms beyond the KB is a confound, so the
stripping is done by one function and listed in full in STRIP.

LEAKS
-----
`prepare.py` writes `.bench-meta.json` into every work tree and it contains
gold_files. An agent that reads it is handed the answer. It is deleted here
before any run starts, in both arms. This is the third time a leak in this
experiment arrived through a file nobody thought of as input; the pattern is
always that the harness left its own notes where the subject could read them.
"""

import argparse
import concurrent.futures as cf
import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import sys

# The arms `build()` knows how to materialise. Anything outside this set would
# fall through its two branches untouched and be a relabelled `kb`.
ARMS = {"none", "kb", "kb-mig"}

# Everything that makes a checkout KB-aware. Removed together for the `none`
# arm so the only difference between arms is the knowledge base itself.
STRIP = [
    "docs/kb",
    ".claude/hooks/check-kb-compiled.sh",
    ".claude/settings.json",
    ".claude/skills/kb-compile",
]

# The CLAUDE.md section that points at the KB. Left in place, the `none` arm
# would be told to read articles that are not there, which is not "no KB", it
# is a broken repo - and a broken repo is an easy win for the other arm.
def strip_claude_md(text):
    """Remove the KB from CLAUDE.md without removing anything else.

    Naive section-deletion is wrong here. Across five months of this repo's
    history `docs/kb` is named from 30 different sections, including
    "Data Contract Discipline (MANDATORY)" and "Test Discipline (MANDATORY)" -
    real guidance that has nothing to do with the knowledge base. Dropping
    those sections would hand the `kb` arm a win that is really the `none` arm
    being deprived of its test policy.

    The heading level also moves: 32 trees carry `### Knowledge Base` and 8
    carry `## Knowledge Base`, so the level is read off the line rather than
    assumed.

    So: delete the dedicated Knowledge Base section outright, and elsewhere
    delete only the LINES that point at articles.
    """
    lines = text.splitlines()
    out, skip_to = [], None
    for line in lines:
        m = re.match(r"^(#{1,6}) +(.*)", line)
        if m:
            level = len(m.group(1))
            if skip_to is not None and level <= skip_to:
                skip_to = None
            if skip_to is None and m.group(2).strip().lower() == "knowledge base":
                skip_to = level
                continue
        if skip_to is not None:
            continue
        if "docs/kb" in line:
            line = strip_kb_from_line(line)
            if line is None:
                continue
        out.append(line)
    return "\n".join(out).rstrip() + "\n"


# A markdown link into the KB, and a bare article path.
KB_LINK = re.compile(r"\[[^\]]*\]\(docs/kb/[^)]*\)|`?docs/kb/[\w.\-/]*`?")


def strip_kb_from_line(line):
    """Remove the KB reference from one line, keeping whatever else it said.

    Dropping the whole line is too blunt. This is a real example from the
    repo's PR policy:

        **PR body (MANDATORY):** ... Buckets: **Tests** = `test/|...`; ...
        Generator one-liner + exact path globs in [`arch-release-process.md`]
        (docs/kb/arch-release-process.md) § Change-footprint generator.

    All of that is policy the `none` arm should still be held to; only the
    final sentence points at an article. Removing the line would take the
    bucket definitions with it and quietly make `none` the arm with less
    guidance rather than the arm with no KB.

    Returns None when nothing substantive survives - a Quick Lookup table row
    is nothing BUT a pointer, and an empty row is worse than no row.
    """
    if line.lstrip().startswith("|"):
        return None
    parts = re.split(r"(?<=[.!?])\s+", line)
    kept = [x for x in parts if "docs/kb" not in x]
    rebuilt = " ".join(kept).strip() if len(parts) > 1 else KB_LINK.sub("", line)
    # Nothing left but bullets, bold markers and punctuation.
    if not re.sub(r"[\s\-*_:|#.>`]", "", rebuilt):
        return None
    return rebuilt.rstrip()


PROMPT = """A bug has been reported against this repository:

<report>
{report}
</report>

Investigate the codebase and implement a fix. Make the actual edits."""


def run(cmd, cwd=None, **kw):
    return subprocess.run(cmd, cwd=cwd, capture_output=True, text=True, **kw)


class CellRefused(Exception):
    """One cell cannot be built, and only that cell is lost.

    A plain exception rather than SystemExit because this is raised inside a
    ThreadPoolExecutor worker: `f.result()` re-raises it in the main thread,
    and SystemExit there aborted every other task's arms too, after their
    results had already been computed and written but before the summary
    printed.

    The `none` arm's two strip assertions are the same shape and used to raise
    SystemExit from the same worker threads, so the fix covered one branch and
    left the one beside it - a CLAUDE.md whose KB heading `strip_claude_md`
    does not recognise still took the whole run down.
    """


class MigrationMissing(CellRefused):
    """The kb-mig arm has no migrated article for this tree."""


class StripFailed(CellRefused):
    """The none arm's KB removal did not remove the KB."""


def apply_migration(dest, cache):
    """Swap each article for its migrated form, keyed on the original's sha1.

    Strict: a tree with any unmigrated article is refused rather than run.
    A half-migrated KB is a third thing that is neither arm, and it would be
    invisible in the results - the run would simply look like a weak effect.

    Resolved in full before anything is written. The earlier version wrote as
    it went and raised on the first gap, which left `dest` holding a mix of
    migrated and original articles - the half-migrated third thing this
    refuses to run, created by the refusal itself.
    """
    kb = dest / "docs/kb"
    # `Path.glob` on a missing directory yields nothing rather than raising, so
    # a tree with no KB gave pending == missing == [] and sailed past the
    # strict refusal below as an unmodified full-KB tree labelled kb-mig - the
    # "third thing that is neither arm" this function exists to refuse, in the
    # one shape its own loop cannot see.
    if not kb.is_dir():
        raise MigrationMissing(f"{dest}: no docs/kb, so there is nothing to "
                               "migrate and this is not the kb-mig arm")
    pending, missing = [], []
    for art in sorted(kb.glob("*.md")):
        if art.name in ("INDEX.md", "DIGEST.md"):
            continue
        key = hashlib.sha1(art.read_bytes()).hexdigest()
        src = cache / f"{key}.md"
        if src.exists():
            pending.append((art, src))
        else:
            missing.append(art.name)
    if missing:
        # Not "run migrate.py first": the usual cause is that it WAS run,
        # against a different --root, and "run it first" sends someone to
        # repeat the step they just completed. The two defaults now agree, so
        # this fires for an explicitly mismatched --root rather than for the
        # documented sequence typed with bare flags.
        raise MigrationMissing(
            f"{dest}: {len(missing)} article(s) absent from {cache}: "
            f"{missing[:3]} - migrate this tree's --root, not another's")
    for art, src in pending:
        art.write_text(src.read_text())


def build(work, dest, arm, migrated=None):
    """Materialise one run directory. Identical across arms but for the KB."""
    if dest.exists():
        shutil.rmtree(dest)
    shutil.copytree(work, dest, symlinks=True)

    # The harness's own notes name the gold files. Both arms, always.
    (dest / ".bench-meta.json").unlink(missing_ok=True)

    if arm == "kb-mig":
        apply_migration(dest, migrated)

    if arm == "none":
        for rel in STRIP:
            p = dest / rel
            shutil.rmtree(p) if p.is_dir() else p.unlink(missing_ok=True)
        cm = dest / "CLAUDE.md"
        if cm.exists():
            stripped = strip_claude_md(cm.read_text())
            if "docs/kb" in stripped:
                # Fail loudly, but only this cell. A silent no-op here leaves
                # the `none` arm being told to read articles that are not there.
                raise StripFailed(f"{dest}: docs/kb survived CLAUDE.md strip")
            cm.write_text(stripped)
        leftover = [str(p.relative_to(dest)) for p in dest.rglob("*")
                    if "docs/kb" in str(p.relative_to(dest))]
        if leftover:
            raise StripFailed(f"{dest}: KB survived stripping: {leftover[:3]}")

    # A fresh single-commit repo: gives a diff to grade, with no history to
    # leak, since the only commit is the pre-fix tree we just laid down.
    run(["git", "init", "-q", "-b", "base"], cwd=dest)
    run(["git", "add", "-A", "-f"], cwd=dest)
    run(["git", "-c", "user.email=b@b", "-c", "user.name=bench",
         "commit", "-q", "-m", "base", "--no-verify"], cwd=dest)


def execute(task, arm, seed, root, runs, model, turns, timeout, migrated=None):
    out = runs / task["instance_id"] / f"{arm}-{seed}"
    done = out / "result.json"
    if done.exists():
        return json.loads(done.read_text())

    build(root / task["instance_id"], out, arm, migrated)
    env = {**os.environ, "CLAUDE_PROJECT_DIR": str(out)}
    cmd = ["claude", "-p", PROMPT.format(report=task["problem_statement"]),
           "--output-format", "json", "--model", model,
           "--max-turns", str(turns),
           "--permission-mode", "bypassPermissions",
           # No route to the upstream repository, where the real fix lives.
           "--disallowed-tools", "WebSearch", "WebFetch"]
    try:
        r = run(cmd, cwd=out, env=env, timeout=timeout)
        meta = json.loads(r.stdout) if r.stdout.strip().startswith("{") else {}
        err = None if r.returncode == 0 else (r.stderr or "")[-400:]
    except subprocess.TimeoutExpired:
        meta, err = {}, "timeout"

    diff = run(["git", "diff"], cwd=out).stdout
    touched = run(["git", "diff", "--name-only"], cwd=out).stdout.split()
    (out / "patch.diff").write_text(diff)
    rec = {
        "instance_id": task["instance_id"], "arm": arm, "seed": seed,
        "error": err, "touched": touched, "diff_lines": len(diff.splitlines()),
        "cost_usd": meta.get("total_cost_usd"),
        "turns": meta.get("num_turns"), "ms": meta.get("duration_ms"),
        "text": (meta.get("result") or "")[:4000],
    }
    done.write_text(json.dumps(rec, indent=2) + "\n")
    print(f"  {task['instance_id']:<20} {arm:<5} s{seed}  "
          f"{len(touched):>2} file(s), {rec['diff_lines']:>4} diff lines"
          + (f"  ERR {err[:60]}" if err else ""), flush=True)
    return rec


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--tasks", default="bench/tasks-vv.json")
    ap.add_argument("--root", default="bench/work-vv")
    ap.add_argument("--runs", default="bench/runs")
    ap.add_argument("--model", default="sonnet")
    ap.add_argument("--limit", type=int, default=8)
    ap.add_argument("--seeds", type=int, default=1)
    ap.add_argument("--turns", type=int, default=60)
    ap.add_argument("--timeout", type=int, default=900)
    ap.add_argument("--jobs", type=int, default=4)
    ap.add_argument("--arms", default="none,kb",
                    help="comma-separated: none, kb, kb-mig")
    ap.add_argument("--migrated", default="bench/migrated",
                    help="cache written by migrate.py, for the kb-mig arm")
    a = ap.parse_args()

    tasks = json.loads(pathlib.Path(a.tasks).read_text())
    root, runs = pathlib.Path(a.root), pathlib.Path(a.runs)
    tasks = [t for t in tasks if (root / t["instance_id"]).exists()][: a.limit]
    runs.mkdir(parents=True, exist_ok=True)

    # Validated, because `build()` branches on "none" and "kb-mig" with no
    # else: an unrecognised name silently produces an unmodified full-KB tree,
    # i.e. a second copy of the `kb` arm wearing a different label. judge.py
    # takes the label off the directory name and prints it a mean and a
    # p-value, so a typo buys a full paid run reported as a third condition
    # that is really `kb` measured twice. Nothing downstream can tell.
    arms = [x.strip() for x in a.arms.split(",") if x.strip()]
    unknown = [x for x in arms if x not in ARMS]
    if unknown:
        raise SystemExit(f"unknown arm(s) {unknown}; choose from "
                         f"{sorted(ARMS)}")
    mig = pathlib.Path(a.migrated)
    jobs = [(t, arm, s) for t in tasks for arm in arms for s in range(a.seeds)]
    print(f"{len(jobs)} run(s): {len(tasks)} task(s) x {len(arms)} arm(s) "
          f"({', '.join(arms)}) x {a.seeds} seed(s), model={a.model}\n",
          flush=True)

    with cf.ThreadPoolExecutor(max_workers=a.jobs) as ex:
        futs = [(ex.submit(execute, t, arm, s, root, runs, a.model, a.turns,
                           a.timeout, mig), t, arm, s) for t, arm, s in jobs]
        # Per cell. One tree with no migration used to take down every other
        # task's arms, after their results were computed and written to disk
        # but before the summary that interprets them printed - so the run
        # cost full price and reported nothing.
        #
        # Every exception is caught, not just the refusals this was written
        # for. `execute` can also raise JSONDecodeError on a result.json left
        # truncated by an interrupted pass - which is hit by the resume path,
        # the entire point of the `done.exists()` cache - and OSError if
        # `claude` is not on PATH. Each of those did exactly the damage
        # described above, through a door left open beside the one just shut.
        recs, refused = [], []
        for f, t, arm, s in futs:
            try:
                recs.append(f.result())
            except CellRefused as e:
                refused.append(f"{t['instance_id']} {arm} s{s}: {e}")
            except Exception as e:  # noqa: BLE001 - one cell, never the run
                refused.append(
                    f"{t['instance_id']} {arm} s{s}: "
                    f"{type(e).__name__}: {e}")

    empty = [r for r in recs if not r["touched"]]
    print(f"\n{len(recs)} run(s) complete; {len(empty)} produced no edit")
    if refused:
        # Loudly, and never folded into the arm medians below. A refused cell
        # is a missing measurement, and counting it as anything would make the
        # arm look weak rather than unmeasured.
        print(f"{len(refused)} run(s) REFUSED and are absent from the "
              f"comparison:")
        for r in refused:
            print(f"  {r}")
    for arm in arms:
        rs = [r for r in recs if r["arm"] == arm and r["touched"]]
        if rs:
            print(f"  {arm:<5} {len(rs):>2} with edits, "
                  f"median {sorted(len(r['touched']) for r in rs)[len(rs)//2]} "
                  f"file(s), ${sum(r['cost_usd'] or 0 for r in rs):.2f}")
    # Non-zero when any cell is missing, like migrate.py. `agent.py && judge.py`
    # is the shape run.sh already uses, and returning 0 after a whole arm was
    # refused let grading proceed on a corpus the judge then reported as a
    # clean two-arm result - the refusal notice having scrolled past an hour
    # earlier behind the per-run progress output.
    return 1 if refused else 0


if __name__ == "__main__":
    sys.exit(main())
