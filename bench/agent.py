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
    none   the repository with its knowledge base removed - articles, the
           CLAUDE.md section pointing at them, the compiled-gate hook, and the
           kb-compile skill. Everything else byte-identical.
    kb     the repository as it actually ships.

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
import json
import os
import pathlib
import re
import shutil
import subprocess
import sys

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


def build(work, dest, arm):
    """Materialise one run directory. Identical in both arms but for STRIP."""
    if dest.exists():
        shutil.rmtree(dest)
    shutil.copytree(work, dest, symlinks=True)

    # The harness's own notes name the gold files. Both arms, always.
    (dest / ".bench-meta.json").unlink(missing_ok=True)

    if arm == "none":
        for rel in STRIP:
            p = dest / rel
            shutil.rmtree(p) if p.is_dir() else p.unlink(missing_ok=True)
        cm = dest / "CLAUDE.md"
        if cm.exists():
            stripped = strip_claude_md(cm.read_text())
            if "docs/kb" in stripped:
                # Fail loudly. A silent no-op here leaves the `none` arm being
                # told to read articles that are not there.
                raise SystemExit(f"{dest}: docs/kb survived CLAUDE.md strip")
            cm.write_text(stripped)
        leftover = [str(p.relative_to(dest)) for p in dest.rglob("*")
                    if "docs/kb" in str(p.relative_to(dest))]
        if leftover:
            raise SystemExit(f"{dest}: KB survived stripping: {leftover[:3]}")

    # A fresh single-commit repo: gives a diff to grade, with no history to
    # leak, since the only commit is the pre-fix tree we just laid down.
    run(["git", "init", "-q", "-b", "base"], cwd=dest)
    run(["git", "add", "-A", "-f"], cwd=dest)
    run(["git", "-c", "user.email=b@b", "-c", "user.name=bench",
         "commit", "-q", "-m", "base", "--no-verify"], cwd=dest)


def execute(task, arm, seed, root, runs, model, turns, timeout):
    out = runs / task["instance_id"] / f"{arm}-{seed}"
    done = out / "result.json"
    if done.exists():
        return json.loads(done.read_text())

    build(root / task["instance_id"], out, arm)
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
    a = ap.parse_args()

    tasks = json.loads(pathlib.Path(a.tasks).read_text())
    root, runs = pathlib.Path(a.root), pathlib.Path(a.runs)
    tasks = [t for t in tasks if (root / t["instance_id"]).exists()][: a.limit]
    runs.mkdir(parents=True, exist_ok=True)

    jobs = [(t, arm, s) for t in tasks for arm in ("none", "kb")
            for s in range(a.seeds)]
    print(f"{len(jobs)} run(s): {len(tasks)} task(s) x 2 arms x {a.seeds} seed(s)"
          f", model={a.model}\n", flush=True)

    with cf.ThreadPoolExecutor(max_workers=a.jobs) as ex:
        futs = [ex.submit(execute, t, arm, s, root, runs, a.model, a.turns,
                          a.timeout) for t, arm, s in jobs]
        recs = [f.result() for f in futs]

    empty = [r for r in recs if not r["touched"]]
    print(f"\n{len(recs)} run(s) complete; {len(empty)} produced no edit")
    for arm in ("none", "kb"):
        rs = [r for r in recs if r["arm"] == arm and r["touched"]]
        if rs:
            print(f"  {arm:<5} {len(rs):>2} with edits, "
                  f"median {sorted(len(r['touched']) for r in rs)[len(rs)//2]} "
                  f"file(s), ${sum(r['cost_usd'] or 0 for r in rs):.2f}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
