#!/usr/bin/env python3
"""Tier 1: can the KB's routing find the files the fix had to touch?

Deterministic, no model, seconds to run. This is the one that belongs in the
development loop, because it tests the ARTIFACT rather than an agent's mood.

WHAT IT ASKS
------------
Given an issue as a human wrote it, rank the repository's files by "you should
look here". Score against the files the real fix edited. Two arms:

  paths   the control. Lexical match between the issue and file paths. This is
          the "plain directory structure" baseline - what you get for free from
          a tree, with no KB at all.

  kb      match the issue against each article's headline claims, then credit
          the files that article owns. This is what an agent routing off
          DIGEST.md and the reverse index is actually doing.

If `kb` does not beat `paths`, the articles are filing rather than describing
and no amount of delivery will save them.

WHY RECALL AND NOT ACCURACY
---------------------------
Shepard & Albrecht (arXiv:2606.20512) found repository guidance improves
coverage - reaching the correct file - while per-patch precision stays flat.
Recall@k is that quantity stated directly. Gloaguen et al. (arXiv:2602.11988)
found task success does not move, so measuring success would spend a lot to
observe nothing.

A caveat this metric cannot escape: the gold set is the files the patch EDITED,
which under-counts files you had to READ to know what to edit. It is a floor on
the real routing question, not the whole of it. ContextBench (arXiv:2602.05892)
pays humans to annotate the fuller set; this is the free approximation.
"""

import argparse
import collections
import json
import math
import pathlib
import random
import re
import sys

WORD = re.compile(r"[A-Za-z_][A-Za-z0-9_]{2,}")
# Tokens that appear in every Python traceback and carry no routing signal.
STOP = {
    "the", "and", "for", "that", "this", "with", "from", "not", "but", "you",
    "have", "are", "was", "when", "should", "would", "which", "there", "then",
    "line", "file", "self", "none", "true", "false", "return", "import",
    "python", "code", "error", "issue", "bug", "test", "tests", "expected",
    "actual", "result", "example", "traceback", "https", "http", "com",
}


def tokens(text):
    return [w.lower() for w in WORD.findall(text or "")
            if w.lower() not in STOP]


def path_tokens(path):
    """A path is a sentence: split on separators and camelCase."""
    parts = re.split(r"[/._-]", path)
    out = []
    for p in parts:
        out.extend(re.findall(r"[A-Z]?[a-z0-9]+|[A-Z]+(?![a-z])", p))
    return [w.lower() for w in out if len(w) > 2 and w.lower() not in STOP]


def idf(docs):
    n = len(docs) or 1
    df = collections.Counter()
    for d in docs:
        df.update(set(d))
    return {w: math.log(1 + n / (1 + c)) for w, c in df.items()}


def score(query, doc, weights):
    q, d = set(query), set(doc)
    return sum(weights.get(w, 1.0) for w in q & d)


def repo_files(root):
    out = []
    for p in root.rglob("*"):
        if not p.is_file() or ".git" in p.parts or ".bench-meta" in p.name:
            continue
        rel = p.relative_to(root).as_posix()
        if rel.endswith((".py", ".js", ".ts", ".go", ".java", ".rs", ".dart")):
            out.append(rel)
    return out


def load_kb(root):
    """Article -> (claim tokens, owned files), from the generated digest.

    Reads DIGEST.md and .reverse-index.json because those are what an agent
    routing through the KB actually consults. If the repo has no KB this
    returns nothing and the kb arm is reported as absent rather than as zero -
    an unmeasured arm and a failing arm are different findings.
    """
    # A KB is its ARTICLES. Detecting it by DIGEST.md or .reverse-index.json
    # means detecting a build output, and the longest-lived KB available has
    # neither at most of its commits - it was reported as "no KB" forty times
    # while sitting there with thirty-five articles in it. Third time this
    # assumption has bitten in this harness; the source is the thing.
    kbdir = None
    for cand in ("docs/kb", "knowledge-base", ".kb/articles", "docs/knowledge"):
        d = root / cand
        if d.is_dir() and any(
                f.stem not in ("INDEX", "DIGEST") for f in d.glob("*.md")):
            kbdir = d
            break
    if kbdir is None:
        return None
    owners = {}
    idx = kbdir / ".reverse-index.json"
    if idx.exists():
        data = json.loads(idx.read_text()).get("owners", {})
        for f, meta in data.items():
            for a in meta.get("articles", []):
                owners.setdefault(a, set()).add(f)
    else:
        # No generated index: ownership comes from each article's own
        # `covers:` frontmatter and `## FILES` block, which is where the
        # index would have come from anyway. Patterns, expanded later against
        # the real file list, because `covers:` takes globs.
        for path in sorted(kbdir.glob("*.md")):
            if path.stem in ("INDEX", "DIGEST"):
                continue
            owners.setdefault(path.stem, set()).update(article_patterns(path))
    # Claims are kept WITH their section label, because the live question is
    # whether instructions and overviews behave differently. Gloaguen et al.
    # (arXiv:2602.11988) found "instructions in the context files are well
    # followed by coding agents, repository overviews ... are not helpful".
    # INVARIANT and GOTCHA are instructions; DECISION is an overview.
    if not (kbdir / "DIGEST.md").exists():
        return load_kb_articles(kbdir, owners)

    claims = {}
    current = None
    for line in (kbdir / "DIGEST.md").read_text().splitlines():
        m = re.match(r"^## (\S+)\s*$", line)
        if m:
            current = m.group(1)
            claims[current] = []
            continue
        if not current or not line.startswith("- "):
            continue
        lm = re.match(r"^- \*\*([A-Z ]+)\*\*:", line)
        section = lm.group(1).strip() if lm else "OTHER"
        claims[current].append((section, tokens(line)))
    return {a: (claims.get(a, []), owners.get(a, set())) for a in claims}


COVERS = re.compile(r"^covers:\s*(.*)$", re.M)
BACKTICK_PATH = re.compile(r"`([^`]*/[^`]+)`")


def article_patterns(path):
    """Ownership as the article states it: `covers:` plus `## FILES`."""
    text = path.read_text(errors="replace")
    out = set()
    # Only split on frontmatter when the file actually OPENS with it. An
    # article with no frontmatter - the older generation has none - contains
    # `---` as an ordinary horizontal rule, and splitting on that took
    # everything after the rule as the body, silently discarding the `## FILES`
    # block above it. Every such article then owned nothing, and reported it as
    # zero rather than as unparsed.
    if text.startswith("---"):
        head = text.split("---", 2)
        front = head[1] if len(head) > 2 else ""
        body = head[2] if len(head) > 2 else text
    else:
        front, body = "", text
    m = COVERS.search(front)
    if m:
        inline = m.group(1).strip()
        if inline.startswith("["):
            out.update(x.strip() for x in inline.strip("[]").split(",") if x.strip())
        else:
            for line in front[m.end():].splitlines():
                t = line.strip()
                if t.startswith("- "):
                    out.add(t[2:].strip())
                elif t and not t.startswith("#"):
                    break
    in_files = in_fence = False
    for line in body.splitlines():
        if line.startswith("```"):
            in_fence = not in_fence
            continue
        if not in_fence and line.startswith("## "):
            in_files = line.strip() == "## FILES"
            continue
        if not in_files:
            continue
        # Two article generations to read. The current format backticks paths
        # in a list or a table. The one this tool was ported from - still live
        # in a repo five months on - has no frontmatter at all and writes
        # `## FILES` as a fenced block of `path   # what it is` lines. Both are
        # ownership claims; only the spelling changed.
        found = BACKTICK_PATH.findall(line)
        if found:
            out.update(found)
            continue
        tok = line.strip().split("#", 1)[0].strip().lstrip("-*| ").strip()
        if tok and ("/" in tok or re.search(r"\.[A-Za-z0-9]{1,5}$", tok)) \
                and " " not in tok:
            out.add(tok)
    return {o for o in out if o}


def expand(patterns, files):
    """`dir/**` is a prefix; a bare `*` does not cross a slash."""
    hit = set()
    for pat in patterns:
        if pat.endswith("/**"):
            pre = pat[:-2]
            hit.update(f for f in files if f.startswith(pre))
        elif "*" in pat:
            rx = re.compile("^" + re.escape(pat).replace(r"\*", "[^/]*") + "$")
            hit.update(f for f in files if rx.match(f))
        elif pat in files:
            hit.add(pat)
    return hit


# Bold lead at the start of a claim: `**The thing that must stay true.** ...`
BOLD_LEAD = re.compile(r"^\s*(?:[-*]\s*)?\*\*(.+?)\*\*")


def load_kb_articles(kbdir, owners):
    """Read claims from the articles when no digest was generated.

    The KB that this tool was ported from ran for five months without ever
    emitting a digest - it had a reverse index and articles and nothing else.
    Requiring DIGEST.md would have made the longest-lived KB available
    invisible to the harness, reported as "no KB" rather than measured, which
    is the empty-versus-failing confusion again.

    Reading articles is also the less lossy measurement: the digest caps each
    section at three claims, so an article with fifteen invariants reaches it
    with three. Where both exist the digest is preferred anyway, because the
    digest is what an agent actually routes off.
    """
    claims = {}
    for path in sorted(kbdir.glob("*.md")):
        aid = path.stem
        if aid in ("INDEX", "DIGEST"):
            continue
        section, got = None, []
        for line in path.read_text(errors="replace").splitlines():
            if line.startswith("## "):
                section = line[3:].strip()
                continue
            if not section:
                continue
            m = BOLD_LEAD.match(line)
            if m:
                got.append((section, tokens(m.group(1))))
        if got:
            claims[aid] = got
    return {a: (claims.get(a, []), owners.get(a, set()))
            for a in set(claims) | set(owners)}


def rank_paths(issue, files, weights):
    q = tokens(issue)
    return sorted(files, key=lambda f: -score(q, path_tokens(f), weights))


# The arms. `sections=None` uses every claim; the others ablate the digest
# down to one category, which is the instructions-versus-overview split stated
# in terms this tool can act on.
ARMS = {
    "kb": None,
    "kb-inst": {"INVARIANT", "GOTCHA"},
    "kb-over": {"DECISION"},
}


def rank_kb(issue, files, kb, weights, sections=None, cap=None, seed=0):
    """cap subsamples each article's claims, to control for VOLUME.

    The instruction arm carries roughly twice the claims of the overview arm,
    because articles state more invariants than decisions. Without a cap,
    "instructions beat overviews" is indistinguishable from "more text beats
    less text", and the two have opposite implications for what to write.
    """
    q = tokens(issue)
    per_file = collections.defaultdict(float)
    for aid, (claims, patterns) in kb.items():
        owned = expand(patterns, files) if not (patterns & set(files)) else patterns
        picked = [c for c in claims
                  if sections is None or c[0] in sections]
        if cap is not None and len(picked) > cap:
            rnd = random.Random(f"{seed}:{aid}")
            picked = rnd.sample(picked, cap)
        s = 0.0
        for _section, toks in picked:
            s = max(s, score(q, toks, weights))
        if s <= 0:
            continue
        for f in owned:
            per_file[f] = max(per_file[f], s)
    # Articles break ties among the files they own; path match orders within.
    return sorted(files, key=lambda f: (-per_file.get(f, 0.0),
                                        -score(q, path_tokens(f), weights)))


def rank_articles(issue, files, kb, weights, sections=None):
    """Rank ARTICLES, which is the unit a KB actually routes to.

    File-level recall is unfair to a coarse KB and the unfairness is not small.
    Every file an article owns gets that article's claim score, so a matched
    74-file article puts 74 files in one tie and the gold file lands wherever
    path-matching happens to drop it inside the block. Measured on a five-month
    KB: median 13 files share the top score, max 158, and the median rank of
    the first gold file is 36. R@5 over that is a coin toss about tie order.

    An agent does not read 74 files. It reads the ARTICLE, which says which
    part to open. Scoring the routing step on its own terms means asking
    whether the right article was named.
    """
    q = tokens(issue)
    scored = []
    for aid, (claims, patterns) in kb.items():
        picked = [c for c in claims if sections is None or c[0] in sections]
        s = max([score(q, tk, weights) for _sec, tk in picked] or [0.0])
        scored.append((s, aid))
    return [aid for s, aid in sorted(scored, key=lambda x: -x[0])]


def articles_owning(kb, files, gold):
    """Which articles would have routed you to a gold file."""
    fs = set(files)
    out = set()
    for aid, (_claims, patterns) in kb.items():
        owned = expand(patterns, fs) if not (patterns & fs) else patterns
        if owned & set(gold):
            out.add(aid)
    return out


def rank_articles_by_paths(issue, files, kb, weights):
    """Control: what the tree alone would say about which subsystem.

    Each article scored by the best path-match among the files it owns, so the
    comparison is like for like - both arms answer "which article", one using
    its claims and one using nothing but filenames.
    """
    q = tokens(issue)
    fs = set(files)
    scored = []
    for aid, (_claims, patterns) in kb.items():
        owned = expand(patterns, fs) if not (patterns & fs) else patterns
        s = max([score(q, path_tokens(f), weights) for f in owned] or [0.0])
        scored.append((s, aid))
    return [aid for s, aid in sorted(scored, key=lambda x: -x[0])]


def evaluate(ranked, gold, ks):
    got = {}
    for k in ks:
        top = set(ranked[:k])
        got[k] = len([g for g in gold if g in top]) / len(gold)
    rr = 0.0
    for i, f in enumerate(ranked, 1):
        if f in gold:
            rr = 1.0 / i
            break
    return got, rr


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--tasks", default="bench/tasks.json")
    ap.add_argument("--root", default="bench/work")
    ap.add_argument("--ks", default="1,5,10,25")
    ap.add_argument("--json", help="write per-task results here")
    args = ap.parse_args()

    ks = [int(k) for k in args.ks.split(",")]
    tasks = json.loads(pathlib.Path(args.tasks).read_text())
    root = pathlib.Path(args.root)

    arm_names = ("paths", *ARMS)
    agg = {arm: {"n": 0, "rr": 0.0, **{k: 0.0 for k in ks}}
           for arm in arm_names}
    rows, art_rows, no_kb = [], [], 0

    for t in tasks:
        work = root / t["instance_id"]
        if not work.exists():
            continue
        files = repo_files(work)
        if not files:
            continue
        weights = idf([path_tokens(f) for f in files])
        gold = [g for g in t["gold_files"] if g in set(files)]
        if not gold:
            continue

        row = {"instance_id": t["instance_id"], "gold": gold, "n_files": len(files)}
        ranked = rank_paths(t["problem_statement"], files, weights)
        hits, rr = evaluate(ranked, gold, ks)
        agg["paths"]["n"] += 1
        agg["paths"]["rr"] += rr
        for k in ks:
            agg["paths"][k] += hits[k]
        row["paths"] = {"rr": rr, **{str(k): hits[k] for k in ks}}

        kb = load_kb(work)
        if kb:
            gold_arts = articles_owning(kb, files, gold)
            if gold_arts:
                ar = {}
                ranked_a = rank_articles_by_paths(t["problem_statement"], files,
                                                  kb, weights)
                hits, rr = evaluate(ranked_a, gold_arts, (1, 3, 5))
                ar["paths"] = {"rr": rr, **{str(k): hits[k] for k in (1, 3, 5)}}
                for arm, sections in ARMS.items():
                    ranked_a = rank_articles(t["problem_statement"], files, kb,
                                             weights, sections)
                    hits, rr = evaluate(ranked_a, gold_arts, (1, 3, 5))
                    ar[arm] = {"rr": rr, **{str(k): hits[k] for k in (1, 3, 5)}}
                art_rows.append(ar)
            # How much material each arm actually had. An arm with no claims
            # to match on scores zero for the same reason a broken one does,
            # and the two are different findings — the empty-versus-failing
            # ambiguity this whole exercise keeps running into.
            for arm, sections in ARMS.items():
                avail = sum(
                    1 for _, (claims, _) in kb.items() for sec, _ in claims
                    if sections is None or sec in sections)
                agg[arm].setdefault("claims", 0)
                agg[arm]["claims"] += avail
                row.setdefault("claims", {})[arm] = avail
            for arm, sections in ARMS.items():
                ranked = rank_kb(t["problem_statement"], files, kb, weights,
                                 sections)
                hits, rr = evaluate(ranked, gold, ks)
                agg[arm]["n"] += 1
                agg[arm]["rr"] += rr
                for k in ks:
                    agg[arm][k] += hits[k]
                row[arm] = {"rr": rr, **{str(k): hits[k] for k in ks}}
        else:
            no_kb += 1
        rows.append(row)

    # Article-level: the routing question on the KB's own terms.
    if art_rows:
        print("\n  ARTICLE-LEVEL — did it name the right article?\n")
        print("  arm        n    " + "  ".join(f"R@{k:<5}" for k in (1, 3, 5)) + "MRR")
        for arm in ("paths", *ARMS):
            rs = [r for r in art_rows if arm in r]
            if not rs:
                continue
            cells = "  ".join(
                f"{sum(r[arm][str(k)] for r in rs) / len(rs):.3f} " for k in (1, 3, 5))
            mrr = sum(r[arm]["rr"] for r in rs) / len(rs)
            print(f"  {arm:<10} {len(rs):<4} {cells}{mrr:.3f}")
        print("\n  File-level below is reported for continuity, but a coarse KB")
        print("  is penalised by tie size there - see rank_articles().")

    print(f"\nscored {agg['paths']['n']} task(s); {no_kb} had no KB to route with\n")
    header = "  arm      n    " + "  ".join(f"R@{k:<5}" for k in ks) + "MRR"
    print(header)
    for arm in arm_names:
        a = agg[arm]
        if not a["n"]:
            print(f"  {arm:<8} -    (no instance carried a KB)")
            continue
        cells = "  ".join(f"{a[k] / a['n']:.3f} " for k in ks)
        print(f"  {arm:<8} {a['n']:<4} {cells}{a['rr'] / a['n']:.3f}")

    # PAIRED only. The two arms run on different task counts whenever some
    # repositories carry a KB and others do not, and subtracting means over
    # different task sets compares the sets, not the arms. This is the same
    # error as reading a table while half the runs are still in flight.
    both = [r for r in rows if "kb" in r and "paths" in r]
    if both:
        k = ks[1]
        dp = sum(r["paths"][str(k)] for r in both) / len(both)
        print(f"\n  paired on {len(both)} task(s) carrying a KB, at R@{k}:")
        print(f"    paths     {dp:.3f}   (control: no KB at all)")
        for arm in ARMS:
            m = sum(r[arm][str(k)] for r in both) / len(both)
            avail = sum(r.get("claims", {}).get(arm, 0) for r in both)
            note = ""
            if avail == 0:
                note = "   <- NO CLAIMS of this kind; arm is empty, not failing"
            print(f"    {arm:<9} {m:.3f}   delta vs paths {m - dp:+.3f}"
                  f"   [{avail} claims]{note}")
        print("  A KB that does not beat plain path matching is filing, not describing.")
        if len(both) < 20:
            print(f"  n={len(both)} is too small to conclude anything. The effect "
                  f"reported in the\n  literature for guidance is a few points; "
                  f"seeing it needs hundreds.")
    else:
        print("\n  No task carried a KB, so there is nothing to compare. Generate "
              "one with\n  kb-init + kb-catchup against bench/work/<instance_id>.")

    if args.json:
        pathlib.Path(args.json).write_text(json.dumps(rows, indent=2) + "\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
