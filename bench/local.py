#!/usr/bin/env python3
"""Build tasks from a repository's own history, for repos that carry a KB.

SWE-bench gives issues and gold patches but no knowledge base; generating one
costs a catchup run per repository. A repo that already HAS a KB has the
expensive half for free, and its history has the rest — every bugfix commit is
a (question, answer) pair where the answer is the set of files it touched.

THE LEAK, AND THE GUARD
-----------------------
A commit message is written AFTER the fix by someone who knows it. Using it as
the question hands over the answer, which is the failure that cost sixteen runs
last time, arriving through prose instead of through a git ref.

So: take only the subject and first paragraph — the part that describes the
symptom rather than the change — and then REFUSE any task whose text still
contains a gold file's path or stem. The refusal is the point. A task that
cannot be shown clean is dropped, not used with a caveat.

This is a weaker question than a real issue: it is a maintainer's summary, not
a user's complaint, and it will read more like the fix than an issue would. It
flatters routing. Treat the numbers as an upper bound and never compare them
against SWE-bench numbers.
"""

import argparse
import json
import pathlib
import re
import subprocess
import sys

DIFF_OLD = re.compile(r"^--- a/(.+)$", re.M)

# Path segments that mean "this file is not the fix". Segment-wise, so `test/`
# matches but `lib/latest_build.dart` does not.
#
# The first version of this list had `test` and `tests` and stopped there,
# which let in every Dart `integration_test/` change - 17 of 40 gold files in
# the first run, the largest single bucket. The benchmark was then mostly
# measuring whether an agent can fix a flaky emulator, which is real work but
# not work a knowledge base about the app's domain has anything to say about.
TEST_SEGMENTS = {"test", "tests", "integration_test", "test_driver", "spec",
                 "__tests__", "e2e", "testdata", "fixtures", "mocks"}

# Changes to how the project is built, released or linted. Legitimate commits,
# but the question "which article should have told you this" does not apply,
# and they are disproportionately huge and multi-concern.
INFRA_PREFIXES = (".github/", "ci/", "scripts/", "tools/", "infra/")
INFRA_NAMES = {"codemagic.yaml", "Makefile", "Dockerfile", "analysis_options.yaml"}


def is_code(path):
    parts = path.split("/")
    if TEST_SEGMENTS & set(parts):
        return False
    if any(p.endswith(("_test", ".test", ".spec")) for p in parts[-1:]):
        return False
    if path.startswith(INFRA_PREFIXES) or parts[-1] in INFRA_NAMES:
        return False
    return True


def git(args, cwd):
    return subprocess.run(["git", *args], cwd=cwd, check=True,
                          capture_output=True, text=True).stdout


def leaks(text, gold):
    """Does the question already contain its own answer?"""
    low = text.lower()
    for g in gold:
        if g.lower() in low:
            return g
        stem = pathlib.Path(g).stem.lower()
        # A stem of three characters or fewer matches half the English
        # language; requiring length avoids refusing every task over the word
        # "app" while still catching a real filename.
        if len(stem) > 3 and re.search(rf"\b{re.escape(stem)}\b", low):
            return g
    return None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("repo")
    ap.add_argument("--out", default="bench/tasks-local.json")
    ap.add_argument("--limit", type=int, default=20)
    ap.add_argument("--scan", type=int, default=250)
    ap.add_argument("--max-gold", type=int, default=3)
    ap.add_argument("--max-diff", type=int, default=400,
                    help="changed lines; above this a commit is usually "
                         "several changes sharing one message")
    ap.add_argument("--kb-dir", default="docs/kb")
    args = ap.parse_args()

    repo = pathlib.Path(args.repo).expanduser()
    shas = git(["log", "--no-merges", f"-{args.scan}", "--format=%H"], repo).split()

    out, refused_leak, refused_nokb = [], 0, 0
    refused_infra = refused_sprawl = 0
    for sha in shas:
        body = git(["log", "-1", "--format=%s%n%n%b", sha], repo)
        # Subject plus first paragraph: the symptom, before the explanation of
        # what was done about it.
        parts = [p for p in body.split("\n\n") if p.strip()]
        question = "\n\n".join(parts[:2]).strip()
        if len(question) < 60:
            continue

        patch = git(["show", "--format=", "-U0", sha], repo)
        touched = DIFF_OLD.findall(patch)
        gold = sorted({f for f in touched
                       if not f.startswith("docs/kb/") and is_code(f)
                       and f.endswith((".py", ".js", ".ts", ".go", ".dart"))})
        if not gold or len(gold) > args.max_gold:
            continue
        # A commit that is mostly infrastructure with one incidental code file
        # is an infrastructure commit. Requiring the code files to be the bulk
        # of it keeps "CI: fix iOS double-boot + drop UDID pinning" out.
        if len(gold) * 2 < len([f for f in touched if not f.startswith("docs/kb/")]):
            refused_infra += 1
            continue
        # Focus. A sprawling diff is several changes wearing one commit message
        # ("Three pre-tag fixes: release guards + ceremony formatting"), and no
        # rubric written from it is answerable by an agent given one paragraph.
        body_lines = len([l for l in patch.splitlines()
                          if l.startswith(("+", "-")) and not l.startswith(("+++", "---"))])
        if body_lines > args.max_diff:
            refused_sprawl += 1
            continue

        parent = git(["rev-parse", f"{sha}^"], repo).strip()
        # The KB has to exist AT THE PARENT, or the run is scoring a knowledge
        # base written after the fact — hindsight wearing a timestamp.
        # "Did a KB exist here?" is not "was there a DIGEST.md". The
        # longest-lived KB available ran five months without one - it had a
        # reverse index and articles and nothing else. Testing for the derived
        # artifact refused every commit in that repo and reported it as absent.
        tree = git(["ls-tree", "--name-only", parent, f"{args.kb_dir}/"], repo)
        names = [pathlib.Path(x).name for x in tree.split()]
        has_articles = any(n.endswith(".md") and n not in ("INDEX.md", "DIGEST.md")
                           for n in names)
        if not has_articles:
            refused_nokb += 1
            continue

        bad = leaks(question, gold)
        if bad:
            refused_leak += 1
            continue

        out.append({
            "instance_id": f"local-{sha[:9]}",
            "repo": str(repo),
            "base_commit": parent,
            "problem_statement": question,
            "gold_files": gold,
            "source_commit": sha,
        })
        if len(out) >= args.limit:
            break

    pathlib.Path(args.out).write_text(json.dumps(out, indent=2) + "\n")
    print(f"wrote {len(out)} task(s) to {args.out}")
    print(f"  refused {refused_leak} for naming their own answer")
    print(f"  refused {refused_nokb} for having no KB at the parent commit")
    print(f"  refused {refused_infra} for being mostly build/CI/test plumbing")
    print(f"  refused {refused_sprawl} for sprawling past --max-diff lines")
    return 0 if out else 1


if __name__ == "__main__":
    sys.exit(main())
