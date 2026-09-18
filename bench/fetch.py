#!/usr/bin/env python3
"""Pull SWE-bench Verified tasks and reduce each to a localisation question.

WHY LOCALISATION AND NOT RESOLVE RATE
-------------------------------------
Two 2026 results decide this, and they point the same way.

Shepard & Albrecht (arXiv:2606.20512) tuned repository guidance on SWE-bench
Verified and found the gain "comes from coverage rather than precision":
refined guidance produced evaluable patches for 14.5pp more instances while
per-patch precision stayed flat at ~59%. Guidance helps an agent REACH THE
RIGHT FILE; it does not help it write a better change once there.

Gloaguen et al. (arXiv:2602.11988) measured task success directly and found
context files do not generally improve it, at >20% more inference cost, for
both LLM-generated and developer-committed files.

So resolve rate is the metric least likely to move and most expensive to
collect - it needs a container and a test suite per instance. Localisation is
the axis the effect actually lives on, and the ground truth is free: the gold
patch names the files that had to change.

WHAT A TASK BECOMES
-------------------
    problem_statement  ->  the question (an issue, as a human wrote it)
    patch              ->  the answer (the set of files it edits)

Nothing else is needed. No Docker, no environment setup, no FAIL_TO_PASS.
"""

import argparse
import json
import pathlib
import re
import sys
import urllib.parse
import urllib.request

ROWS = "https://datasets-server.huggingface.co/rows"
DATASET = "princeton-nlp/SWE-bench_Verified"

# `--- a/path` is the pre-image side of a unified diff. Taking the pre-image
# rather than `+++ b/` means a pure rename counts the file that existed at
# base_commit, which is the one an agent could have found.
DIFF_OLD = re.compile(r"^--- a/(.+)$", re.M)


def rows(offset, length):
    q = urllib.parse.urlencode(
        {"dataset": DATASET, "config": "default", "split": "test",
         "offset": offset, "length": length})
    with urllib.request.urlopen(f"{ROWS}?{q}", timeout=60) as r:
        return json.load(r)["rows"]


def gold_files(patch):
    """The files the real fix edited, which is what 'found it' means here."""
    return sorted(set(DIFF_OLD.findall(patch or "")))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default="bench/tasks.json")
    ap.add_argument("--limit", type=int, default=40)
    ap.add_argument("--per-repo", type=int, default=4,
                    help="cap per repository. A KB has to be generated once "
                         "per repo+commit, so breadth costs far more than "
                         "depth; without a cap the sample is mostly django.")
    ap.add_argument("--max-gold", type=int, default=3,
                    help="skip sprawling patches. A 20-file change is a "
                         "refactor, and 'which file' is not a question it has "
                         "a defensible answer to.")
    args = ap.parse_args()

    out, seen, offset = [], {}, 0
    while len(out) < args.limit and offset < 500:
        batch = rows(offset, 100)
        if not batch:
            break
        offset += len(batch)
        for item in batch:
            r = item["row"]
            gold = gold_files(r.get("patch"))
            if not gold or len(gold) > args.max_gold:
                continue
            repo = r["repo"]
            if seen.get(repo, 0) >= args.per_repo:
                continue
            seen[repo] = seen.get(repo, 0) + 1
            out.append({
                "instance_id": r["instance_id"],
                "repo": repo,
                "base_commit": r["base_commit"],
                "problem_statement": r.get("problem_statement") or "",
                "gold_files": gold,
                "difficulty": r.get("difficulty"),
            })
            if len(out) >= args.limit:
                break

    p = pathlib.Path(args.out)
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(json.dumps(out, indent=2) + "\n")
    by_repo = {}
    for t in out:
        by_repo[t["repo"]] = by_repo.get(t["repo"], 0) + 1
    print(f"wrote {len(out)} tasks to {p}")
    for repo, n in sorted(by_repo.items(), key=lambda kv: -kv[1]):
        print(f"  {n:3d}  {repo}")
    if not out:
        print("no tasks matched; loosen --max-gold", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
