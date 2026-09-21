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

EVERY item must be phrased so that YES means the patch is GOOD. Never write an
item where YES means the patch did something wrong. This is not a style
preference: a mixed-polarity rubric cannot be scored, because nothing
downstream can tell which convention an individual item followed.

  good: "Does the change preserve the existing per-organization branding?"
  good: "Does the change leave unrelated function handlers untouched?"
  BAD:  "Does the change break the branding mechanism?"

Other rules:
- Each item is ONE self-contained yes/no question answerable from a diff alone.
- Describe BEHAVIOUR AND EFFECT, never a specific implementation. "Guards
  against a null session before use" is good. "Adds an `if (s == null)` on
  line 40 of auth.dart" is bad - a different but correct fix must score YES.
- Give each item a "category":
    "work"        - did it actually fix the reported problem, completely,
                    including any second place that had to change with it.
    "preserve"    - did it avoid collateral damage: unrelated behaviour still
                    intact, contracts other code depends on unchanged, tests
                    not deleted, a second occurrence of the bug not left
                    behind.
  Write 2-3 "preserve" items and put the rest in "work".
- If the reference fix touches several files because one change requires
  another, make that dependency its own "work" item. That is the thoroughness
  this rubric exists to detect.

Return ONLY JSON:
{{"items":[{{"id":1,"check":"...","category":"work"}}]}}"""


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

Every item is written so that YES means the patch is good. An item asking
whether something was PRESERVED or LEFT UNTOUCHED is YES when the patch did
indeed leave it alone, including when the patch never went near it.

An empty or trivial patch fails every positive item. Do not give credit for
intent stated in comments; grade what the code does. If the candidate solves
the problem correctly by different means than you expected, that is still YES.

Return ONLY JSON: {{"grades":[{{"id":1,"verdict":"YES","why":"<12 words"}}]}}"""


def extract_json(text):
    r"""Pull one JSON object out of a model reply.

    `re.search(r"\{.*\}", ...)` is greedy and matches from the first brace to
    the LAST one in the whole reply, so a closing sentence containing a brace,
    or a second example object, produces a span that is not valid JSON. It
    also cannot handle a brace inside a string. Scanning with a depth counter
    that knows about strings and escapes returns the first COMPLETE object,
    which is the one the model was asked for.
    """
    if not text:
        return None
    fence = re.search(r"```(?:json)?\s*(\{.*?\})\s*```", text, re.S)
    if fence:
        try:
            return json.loads(fence.group(1))
        except json.JSONDecodeError:
            pass
    start = text.find("{")
    while start != -1:
        depth, instr, esc = 0, False, False
        for i in range(start, len(text)):
            ch = text[i]
            if esc:
                esc = False
                continue
            if ch == "\\" and instr:
                esc = True
            elif ch == '"':
                instr = not instr
            elif not instr and ch == "{":
                depth += 1
            elif not instr and ch == "}":
                depth -= 1
                if depth == 0:
                    try:
                        return json.loads(text[start:i + 1])
                    except json.JSONDecodeError:
                        break
        start = text.find("{", start + 1)
    return None


def ask(prompt, model, timeout=600, tries=3):
    """One judge call, retried on a malformed or empty reply.

    Returns None only after every attempt failed, so a caller can tell "the
    judge could not answer" apart from "the judge said no". A single
    unparseable reply used to raise out of the worker thread and abort the
    whole grading pass, discarding every result alongside it.
    """
    for n in range(tries):
        try:
            r = subprocess.run(
                ["claude", "-p", prompt, "--output-format", "json",
                 "--model", model, "--disallowed-tools", "WebSearch",
                 "WebFetch", "Bash", "Edit", "Write"],
                capture_output=True, text=True, timeout=timeout)
        except subprocess.TimeoutExpired:
            continue
        try:
            text = json.loads(r.stdout).get("result", "")
        except json.JSONDecodeError:
            continue
        out = extract_json(text)
        if out:
            return out
    return None


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
        items = [{"id": i["id"], "check": i["check"]}
                 for i in rubric["items"]]
        out = ask(GRADE_PROMPT.format(report=task["problem_statement"],
                                      patch=patch[:60000],
                                      rubric=json.dumps(items, indent=2)), model)
        if not out:
            return None
    f.write_text(json.dumps(out, indent=2) + "\n")
    return out


def split_score(rubric, grade):
    """Score the two categories separately.

    The aggregate hides the thing most worth knowing. "work" asks whether the
    problem was actually fixed, including the second place that had to change
    with it. "preserve" asks whether anything else was damaged on the way,
    which is the axis that decides whether a change needs review or rework.

    A KB that raises "preserve" without moving "work" is still doing its job.
    """
    cat = {i["id"]: i.get("category", "work") for i in rubric["items"]}
    got = {"work": [0, 0], "preserve": [0, 0]}
    for g in grade.get("grades", []):
        c = cat.get(g["id"], "work")
        if c not in got:
            c = "work"
        got[c][1] += 1
        if str(g.get("verdict", "")).upper().startswith("Y"):
            got[c][0] += 1
    return (got["work"][0] / got["work"][1] if got["work"][1] else None,
            got["preserve"][0] / got["preserve"][1] if got["preserve"][1] else None)


def score(rubric, grade):
    """Fraction of items the candidate got right.

    Every item is YES-is-good by construction, so this is a plain count. It
    used to invert anything flagged `negative`, which silently required the
    generator to phrase those items as the BAD behaviour. It did so for some
    rubrics and not others, so the same verdict meant opposite things in
    different tasks and the collateral-damage column was unreadable. Uniform
    polarity removes the class of error rather than documenting it.
    """
    n = len(rubric["items"])
    if not n:
        return 0.0
    yes = sum(1 for g in grade.get("grades", [])
              if str(g.get("verdict", "")).upper().startswith("Y"))
    return yes / n


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
    arms = sorted({d.name.rsplit("-", 1)[0] for d in graded})
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
    for arm in arms:
        v = by.get(arm, [])
        if v:
            print(f"  {arm:<6} {len(v):<3} {sum(v)/len(v):.3f}")

    for arm in arms:
        rs = [r for r in rows if r["arm"] == arm]
        if not rs:
            continue
        to = sum(1 for r in rs if r["error"] == "timeout")
        empty = sum(1 for r in rs if not r["touched"] and r["error"] != "timeout")
        cost = sum(r["cost_usd"] or 0 for r in rs)
        print(f"  {arm:<6} {to} timeout, {empty} chose no edit, ${cost:.2f}")

    print("\n  arm    fixed it   damaged nothing")
    for arm in arms:
        rs = [r for r in rows if r["arm"] == arm]
        pos = [r["pos"] for r in rs if r["pos"] is not None]
        neg = [r["neg"] for r in rs if r["neg"] is not None]
        if pos:
            print(f"  {arm:<6} {sum(pos)/len(pos):.3f}      "
                  f"{sum(neg)/len(neg):.3f}" if neg else "")

    # Paired, which is the only comparison that means anything: the arms must
    # be compared on the SAME tasks or the difference is between task sets.
    # Paired against the control, which is the only comparison that means
    # anything: the arms must be compared on the SAME tasks or the difference
    # is between task sets. A sign test because the mean of a couple of dozen
    # noisy paired differences invites more confidence than it earns.
    from math import comb
    pairs = {}
    for r in rows:
        pairs.setdefault((r["instance_id"], r["seed"]), {})[r["arm"]] = r

    def signtest(field, a, b):
        d = [p[a][field] - p[b][field] for p in pairs.values()
             if a in p and b in p
             and p[a][field] is not None and p[b][field] is not None]
        if not d:
            return None
        w = sum(1 for x in d if x > 1e-9)
        l = sum(1 for x in d if x < -1e-9)
        n = w + l
        pv = (min(1.0, 2 * sum(comb(n, i) for i in range(max(w, l), n + 1))
                  / 2 ** n) if n else 1.0)
        return len(d), sum(d) / len(d), w, l, len(d) - n, pv

    for arm in arms:
        if arm == "none":
            continue
        print(f"\n  {arm} vs none, paired:")
        for field, label in (("score", "overall"), ("pos", "fixed it"),
                             ("neg", "damaged nothing")):
            r = signtest(field, arm, "none")
            if not r:
                continue
            n, mean, w, l, t, pv = r
            note = "" if pv < 0.05 else "   not distinguishable from noise"
            print(f"    {label:<16} {mean:+.3f}  on {n} pair(s); "
                  f"better {w}, worse {l}, tie {t};  p={pv:.3f}{note}")

    return 0


if __name__ == "__main__":
    sys.exit(main())
