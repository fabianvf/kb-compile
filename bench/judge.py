#!/usr/bin/env python3
"""Grade what the agents produced, against a rubric, blind to which arm made it.

METHOD, AND WHOSE IT IS
-----------------------
This follows SWE Atlas (arXiv:2605.08366), which evaluates coding agents past
issue resolution using rubric-based LLM-as-a-judge. Their design decisions,
adopted here because they measured them:

  - Rubric items are ATOMIC and SELF-CONTAINED, each graded YES/NO on its own
    rather than as a holistic score. They report Cohen's kappa 0.88 against
    human graders with Opus, and kappa ~0.98 self-consistency.
  - NEGATIVE rubrics matter as much as positive ones - a fix that also breaks
    something unrelated is not a good fix.
  - Their headline finding is the reason this file exists at all: frontier
    models pass 60-80% of regression tests but only 20-40% of rubric checks.
    Whether the work is GOOD and whether it PASSES are different questions,
    and only the second one is cheap.

WHAT IS HELD OUT FROM WHOM
--------------------------
The rubric is generated from the bug report and the human's real fix, BEFORE
any candidate is seen and without ever reading the knowledge base. Generating
it from the KB would be marking the KB's homework in the KB's own words; and a
rubric written after seeing the candidates can be shaped, unconsciously, to fit
whichever one the author already prefers.

Grading is blind. The judge sees "candidate patch", never an arm name and never
a path, and the two candidates for a task are graded in independent calls so
neither anchors the other.

THE LIMITATION, STATED UP FRONT
-------------------------------
The gold patch is what one human did, not the only correct fix. A genuinely
good patch that solves the problem differently will lose rubric items it should
not. SWE Atlas handles this with three expert reviewers filtering over-
prescriptive items; there is no such panel here. The mitigation is to instruct
the generator toward behaviour and effect rather than implementation, and to
report the miss rate rather than pretend it is zero. Treat small differences
between arms as noise.
"""

import argparse
import concurrent.futures as cf
import json
import pathlib
import re
import subprocess
import sys

RUBRIC_PROMPT = """You are writing an evaluation rubric for a bug fix.

<bug_report>
{report}
</bug_report>

<reference_fix>
{gold}
</reference_fix>

The reference fix is what a maintainer actually committed. Write 6-10 atomic,
binary rubric items describing what ANY correct and thorough fix for this bug
must do.

Rules, which matter more than coverage:
- Each item is ONE self-contained yes/no question answerable from a diff alone.
- Describe BEHAVIOUR AND EFFECT, never a specific implementation. "Guards
  against a null session before use" is good. "Adds an `if (s == null)` on
  line 40 of auth.dart" is bad - a different but correct fix must be able to
  score YES.
- Include 2-3 items marked negative:true for things a careless fix does wrong:
  breaking an unrelated behaviour, changing a contract other code depends on,
  leaving a second occurrence of the same bug unfixed, deleting a test.
- If the reference fix touches several files because one change requires
  another, make that dependency its own item. That is the thoroughness this
  rubric exists to detect.

Return ONLY JSON: {{"items":[{{"id":1,"check":"...","negative":false}}]}}"""

GRADE_PROMPT = """Grade a candidate bug fix against a rubric.

<bug_report>
{report}
</bug_report>

<candidate_patch>
{patch}
</candidate_patch>

<rubric>
{rubric}
</rubric>

For each rubric item, answer YES or NO based solely on whether the candidate
patch exhibits the described behaviour. Judge each item independently.

For an item with "negative": true, YES means the candidate DID the bad thing.

An empty or trivial patch fails every positive item. Do not give credit for
intent stated in comments; grade what the code does. If the candidate solves
the problem correctly by different means than you expected, that is still YES.

Return ONLY JSON: {{"grades":[{{"id":1,"verdict":"YES","why":"<12 words"}}]}}"""


def ask(prompt, model, timeout=600):
    r = subprocess.run(
        ["claude", "-p", prompt, "--output-format", "json", "--model", model,
         "--disallowed-tools", "WebSearch", "WebFetch", "Bash", "Edit", "Write"],
        capture_output=True, text=True, timeout=timeout)
    try:
        text = json.loads(r.stdout).get("result", "")
    except json.JSONDecodeError:
        return None
    m = re.search(r"\{.*\}", text, re.S)
    return json.loads(m.group()) if m else None


def gold_patch(task, repo):
    """The maintainer's real fix, which lives only in the source repository."""
    return subprocess.run(
        ["git", "show", "--format=", "-U3", task["source_commit"]],
        cwd=repo, capture_output=True, text=True).stdout


def rubric_for(task, repo, cache, model):
    f = cache / f"{task['instance_id']}.json"
    if f.exists():
        return json.loads(f.read_text())
    gold = gold_patch(task, repo)
    if not gold.strip():
        return None
    out = ask(RUBRIC_PROMPT.format(report=task["problem_statement"],
                                   gold=gold[:60000]), model)
    if not out or not out.get("items"):
        return None
    f.parent.mkdir(parents=True, exist_ok=True)
    f.write_text(json.dumps(out, indent=2) + "\n")
    return out


def grade_run(rundir, task, rubric, model):
    f = rundir / "grade.json"
    if f.exists():
        return json.loads(f.read_text())
    patch = (rundir / "patch.diff").read_text()
    if not patch.strip():
        # No edit at all. Every positive item fails by definition; spending a
        # judge call to be told so would only add variance.
        out = {"grades": [{"id": i["id"], "verdict": "NO", "why": "empty patch"}
                          for i in rubric["items"]]}
    else:
        items = [{"id": i["id"], "check": i["check"],
                  "negative": i.get("negative", False)} for i in rubric["items"]]
        out = ask(GRADE_PROMPT.format(report=task["problem_statement"],
                                      patch=patch[:60000],
                                      rubric=json.dumps(items, indent=2)), model)
        if not out:
            return None
    f.write_text(json.dumps(out, indent=2) + "\n")
    return out


def split_score(rubric, grade):
    """Score positive and negative items separately.

    The aggregate hides the thing most worth knowing. Positive items ask "did
    it do the work" - including the cross-file items, where the reference fix
    touched a second file because the first change required it. Negative items
    ask "did it break something else on the way", which is the axis that
    decides whether a change can be merged without someone reading all of it.

    A KB that raises the negative score without moving the positive one is
    still doing its job: it is the difference between a patch that needs
    review and a patch that needs rework.
    """
    neg = {i["id"]: i.get("negative", False) for i in rubric["items"]}
    pos_got = pos_n = neg_got = neg_n = 0
    for g in grade.get("grades", []):
        yes = str(g.get("verdict", "")).upper().startswith("Y")
        if neg.get(g["id"], False):
            neg_n += 1
            neg_got += 0 if yes else 1
        else:
            pos_n += 1
            pos_got += 1 if yes else 0
    return (pos_got / pos_n if pos_n else None,
            neg_got / neg_n if neg_n else None)


def score(rubric, grade):
    """Fraction of items the candidate got right, negatives counted inverted."""
    neg = {i["id"]: i.get("negative", False) for i in rubric["items"]}
    got = 0
    for g in grade.get("grades", []):
        yes = str(g.get("verdict", "")).upper().startswith("Y")
        if neg.get(g["id"], False):
            got += 0 if yes else 1
        else:
            got += 1 if yes else 0
    n = len(rubric["items"])
    return got / n if n else 0.0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--tasks", default="bench/tasks-vv.json")
    ap.add_argument("--runs", default="bench/runs")
    ap.add_argument("--repo", default=None,
                    help="source repo holding the real fix; defaults to the "
                         "task's own repo field")
    ap.add_argument("--cache", default="bench/rubrics")
    ap.add_argument("--model", default="opus")
    ap.add_argument("--jobs", type=int, default=4)
    a = ap.parse_args()

    tasks = {t["instance_id"]: t for t in
             json.loads(pathlib.Path(a.tasks).read_text())}
    runs, cache = pathlib.Path(a.runs), pathlib.Path(a.cache)

    graded = [d for d in sorted(runs.glob("*/*")) if (d / "patch.diff").exists()]
    ids = sorted({d.parent.name for d in graded})
    print(f"{len(ids)} task(s), {len(graded)} run(s) to grade, judge={a.model}\n",
          flush=True)

    def one(iid):
        t = tasks[iid]
        rub = rubric_for(t, a.repo or t["repo"], cache, a.model)
        if not rub:
            print(f"  {iid:<20} SKIP: no rubric", flush=True)
            return []
        out = []
        for d in sorted((runs / iid).iterdir()):
            if not (d / "patch.diff").exists():
                continue
            g = grade_run(d, t, rub, a.model)
            if g is None:
                print(f"  {iid:<20} {d.name:<8} judge failed", flush=True)
                continue
            arm, seed = d.name.rsplit("-", 1)
            # Carry the run's own record forward. An empty patch scores zero
            # either way, but "the agent decided nothing needed changing" and
            # "the harness cut it off at the wall clock" are opposite facts,
            # and a timeout is a property of the budget rather than of the KB.
            meta = {}
            if (d / "result.json").exists():
                meta = json.loads((d / "result.json").read_text())
            out.append({"instance_id": iid, "arm": arm, "seed": int(seed),
                        "score": score(rub, g), "n_items": len(rub["items"]),
                        "pos": split_score(rub, g)[0],
                        "neg": split_score(rub, g)[1],
                        "error": meta.get("error"),
                        "touched": len(meta.get("touched") or []),
                        "cost_usd": meta.get("cost_usd"),
                        "turns": meta.get("turns")})
        if out:
            cells = "  ".join(f"{r['arm']}={r['score']:.2f}" for r in out)
            print(f"  {iid:<20} {cells}", flush=True)
        return out

    with cf.ThreadPoolExecutor(max_workers=a.jobs) as ex:
        rows = [r for rs in ex.map(one, ids) for r in rs]

    pathlib.Path(runs / "scores.json").write_text(json.dumps(rows, indent=2) + "\n")
    print(f"\n  arm    n   rubric score")
    by = {}
    for r in rows:
        by.setdefault(r["arm"], []).append(r["score"])
    for arm in ("none", "kb"):
        v = by.get(arm, [])
        if v:
            print(f"  {arm:<6} {len(v):<3} {sum(v)/len(v):.3f}")

    for arm in ("none", "kb"):
        rs = [r for r in rows if r["arm"] == arm]
        if not rs:
            continue
        to = sum(1 for r in rs if r["error"] == "timeout")
        empty = sum(1 for r in rs if not r["touched"] and r["error"] != "timeout")
        cost = sum(r["cost_usd"] or 0 for r in rs)
        print(f"  {arm:<6} {to} timeout, {empty} chose no edit, ${cost:.2f}")

    print("\n  arm    did the work   broke nothing")
    for arm in ("none", "kb"):
        rs = [r for r in rows if r["arm"] == arm]
        pos = [r["pos"] for r in rs if r["pos"] is not None]
        neg = [r["neg"] for r in rs if r["neg"] is not None]
        if pos:
            print(f"  {arm:<6} {sum(pos)/len(pos):.3f}          "
                  f"{sum(neg)/len(neg):.3f}" if neg else "")

    # Paired, which is the only comparison that means anything: the arms must
    # be compared on the SAME tasks or the difference is between task sets.
    pairs = {}
    for r in rows:
        pairs.setdefault((r["instance_id"], r["seed"]), {})[r["arm"]] = r["score"]
    both = [p for p in pairs.values() if "none" in p and "kb" in p]
    if both:
        d = [p["kb"] - p["none"] for p in both]
        wins = sum(1 for x in d if x > 0.001)
        ties = sum(1 for x in d if abs(x) <= 0.001)
        print(f"\n  paired on {len(both)}: kb - none = {sum(d)/len(d):+.3f}")
        print(f"    kb better {wins}, tie {ties}, none better {len(d)-wins-ties}")
        # A sign test, because the mean of 13 noisy paired differences invites
        # more confidence than it earns. Two-sided, ties excluded, exact.
        n = wins + (len(d) - wins - ties)
        if n:
            from math import comb
            k = max(wins, n - wins)
            pv = min(1.0, 2 * sum(comb(n, i) for i in range(k, n + 1)) / 2 ** n)
            print(f"    sign test on {n} non-tied pair(s): p = {pv:.3f}"
                  + ("" if pv < 0.05 else "  — not distinguishable from noise"))
    return 0


if __name__ == "__main__":
    sys.exit(main())
