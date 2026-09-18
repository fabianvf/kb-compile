#!/usr/bin/env python3
"""Materialise each task's repository at base_commit, with the future removed.

THE FAILURE THIS EXISTS TO PREVENT
----------------------------------
`git clone` brings every ref. Checking out a parent commit detaches HEAD and
leaves `origin/main` pointing at the future in the same object store, one flag
away. An earlier run of this experiment did exactly that: sixteen agents were
asked to diagnose a bug whose fix was reachable in their own clone. At least
two read the fix commit and reported its reasoning back as their diagnosis, one
quoting the commit message's figures verbatim. The runs agreed with each other,
which read as corroboration and was shared contamination.

So isolation is not a setup detail here, it is the measurement. Two modes:

  archive  (default)  `git archive` the tree at base_commit. No .git at all,
                      so there is no future to leak - contamination is
                      impossible by construction rather than by care.

  history             Full clone, main reset to base_commit, remote removed,
                      every other ref deleted, reflog expired, gc --prune=now.
                      Keeps ancestors so a run may legitimately read history,
                      at the cost of needing the check below to be right.

VERIFY THE NEGATIVE
-------------------
"The agent cannot see the answer" is a negative assertion, and a negative
assertion with no positive case is satisfied by any broken setup. So both modes
record the pre-strip tip commit and then assert it is UNRESOLVABLE afterwards.
If that assertion cannot be made, the task is refused rather than run.
"""

import argparse
import json
import pathlib
import shutil
import subprocess
import sys


def git(args, cwd=None, check=True):
    return subprocess.run(["git", *args], cwd=cwd, check=check,
                          capture_output=True, text=True)


def cache_clone(repo, cache):
    """One full clone per repository, reused by every task that needs it.

    A local path is cloned from disk rather than fetched, so a repo that
    already carries a KB can be benchmarked without publishing it anywhere.
    Still a clone and not the original: the run must never be able to write to
    the repository it is measuring.
    """
    local = pathlib.Path(repo).expanduser()
    if local.is_dir():
        dest = cache / ("local__" + local.name)
        src = str(local)
    else:
        dest = cache / repo.replace("/", "__")
        src = f"https://github.com/{repo}.git"
    if dest.exists():
        return dest
    dest.parent.mkdir(parents=True, exist_ok=True)
    print(f"  cloning {repo} (once)", flush=True)
    git(["clone", "--quiet", src, str(dest)])
    return dest


def strip_history(work, tip):
    """Leave ancestors of HEAD reachable and nothing else."""
    git(["remote", "remove", "origin"], cwd=work, check=False)
    refs = git(["for-each-ref", "--format=%(refname)"], cwd=work).stdout.split()
    head = git(["symbolic-ref", "-q", "HEAD"], cwd=work, check=False).stdout.strip()
    for ref in refs:
        if ref != head:
            git(["update-ref", "-d", ref], cwd=work, check=False)
    git(["reflog", "expire", "--expire=now", "--all"], cwd=work, check=False)
    git(["gc", "--quiet", "--prune=now"], cwd=work, check=False)
    # The positive case. `cat-file -t` on a pruned object must fail.
    probe = git(["cat-file", "-t", tip], cwd=work, check=False)
    return probe.returncode != 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--tasks", default="bench/tasks.json")
    ap.add_argument("--root", default="bench/work")
    ap.add_argument("--cache", default="bench/cache")
    ap.add_argument("--mode", choices=["archive", "history"], default="archive")
    ap.add_argument("--limit", type=int, default=0)
    args = ap.parse_args()

    tasks = json.loads(pathlib.Path(args.tasks).read_text())
    if args.limit:
        tasks = tasks[: args.limit]
    root, cache = pathlib.Path(args.root), pathlib.Path(args.cache)
    root.mkdir(parents=True, exist_ok=True)

    ok = refused = 0
    for t in tasks:
        work = root / t["instance_id"]
        if work.exists():
            shutil.rmtree(work)
        src = cache_clone(t["repo"], cache)
        tip = git(["rev-parse", "HEAD"], cwd=src).stdout.strip()

        if args.mode == "archive":
            work.mkdir(parents=True)
            tar = subprocess.run(
                ["git", "archive", "--format=tar", t["base_commit"]],
                cwd=src, capture_output=True, check=True)
            subprocess.run(["tar", "-x", "-C", str(work)], input=tar.stdout,
                           check=True)
            isolated = not (work / ".git").exists()
        else:
            git(["clone", "--quiet", "--no-hardlinks", str(src), str(work)])
            git(["checkout", "--quiet", "-B", "main", t["base_commit"]], cwd=work)
            isolated = strip_history(work, tip)

        if not isolated:
            print(f"  REFUSED {t['instance_id']}: could not prove the future "
                  f"is unreachable", file=sys.stderr)
            shutil.rmtree(work, ignore_errors=True)
            refused += 1
            continue

        # Second positive case, independent of the first and aimed at the same
        # question: the files the fix touches must exist and must NOT already
        # contain the fix. If the tree were the post-fix one, this is where it
        # shows. Cheap, and it fails differently from the ref check.
        missing = [f for f in t["gold_files"] if not (work / f).exists()]
        (work / ".bench-meta.json").write_text(json.dumps({
            "instance_id": t["instance_id"], "repo": t["repo"],
            "base_commit": t["base_commit"], "mode": args.mode,
            "gold_files": t["gold_files"], "gold_files_missing": missing,
        }, indent=2) + "\n")
        ok += 1
        print(f"  {t['instance_id']:<40} {args.mode}"
              + (f"  WARN {len(missing)} gold file(s) absent" if missing else ""))

    print(f"\nprepared {ok}, refused {refused}")
    return 1 if refused else 0


if __name__ == "__main__":
    sys.exit(main())
