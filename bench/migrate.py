#!/usr/bin/env python3
"""Rewrite a knowledge base into the SPEC.md vocabulary, per task tree.

WHY PER TREE AND NOT ONCE
-------------------------
The obvious move is to migrate the repository's KB as it stands today and drop
the result into every task tree. That leaks: the tasks span five months, and
today's articles describe fixes that had not been made at the base commit. An
agent reading them would be told the answer in the KB's own words.

So each tree's OWN articles are migrated, at the commit they existed. Content
is cached by sha1, which costs little to say and a lot to skip: 399 article
instances across 13 trees are 169 distinct texts.

WHAT THE MIGRATION IS FOR
-------------------------
Not tidiness. Gloaguen et al. (arXiv:2602.11988) found that instructions in
context files are well followed by coding agents while repository overviews
are not helpful. In this KB's vocabulary the overview is most of it: 73
`## PATTERN` sections holding parity matrices and reference tables, against 14
INVARIANT and 18 GOTCHA. The migration's job is to turn a constraint that is
currently DESCRIBED into a constraint that is STATED, and to drop description
that carries no constraint at all.

That is also the risk. A rewrite that loses something an agent needed will
score worse, and it should - that is the measurement working. The prompt is
therefore explicit that nothing may be invented, and the verifier below
refuses any article whose frontmatter identity or `covers:` globs changed,
because those drive ownership and a migration that silently re-owns files is
not testable against the original.
"""

import argparse
import concurrent.futures as cf
import hashlib
import json
import pathlib
import re
import subprocess
import sys

PROMPT = """Rewrite this knowledge-base article into a fixed section vocabulary.

<article>
{body}
</article>

THE VOCABULARY. Use only these `## ` headings. Not every article needs all.

  FILES      the source files this article owns
  INVARIANT  something that must stay true. MUST name what BREAKS if it stops
             being true. "X is loaded before Y" is a description; "X must load
             before Y, or the session reads a null token and silently signs
             the user out" is an invariant.
  GOTCHA     a trap that has actually been hit. Cite the incident, bug number
             or commit if the source text has one.
  DECISION   a choice made, and the alternative that was rejected. A decision
             with no rejected alternative is a fact, not a decision.
  VERIFY     how to check the above still holds: the test, the command.
  STEPS      an ordered procedure, for recipe articles.
  COMMANDS   commands a reader will need.
  SEE ALSO   other articles to read next, as markdown links.

WHAT TO DO WITH WHAT IS THERE. The source uses headings like PATTERN,
INTERFACE, SCHEMA, FLOW, ROUTES, and free-form prose titles.

- A table or description that encodes a CONTRACT becomes an INVARIANT stating
  the contract and what breaks without it. Keep the table underneath only if
  the specific values are load-bearing and cannot be summarised.
- A trap, a footgun, a "note that" becomes a GOTCHA.
- A rationale, a "we chose X because" becomes a DECISION with its rejected
  alternative.
- Reference material that constrains nothing - an inventory, a tour, a
  restatement of what the code plainly says - is CUT. This is the point of
  the exercise. An overview that an agent will not act on costs context and
  buys nothing.

RULES, in order of importance:

1. INVENT NOTHING. Every claim must trace to the source text. If the source
   does not say what breaks, say what it does say and keep it shorter, rather
   than inventing a consequence. You are reorganising knowledge, not adding it.
2. Open with YAML frontmatter, which this article does not currently have:

       ---
       id: {stem}
       type: {atype}
       see_also:
         - <article ids the SEE ALSO section links to, if any>
       ---

   Use exactly that id and type. Do not add a `covers:` key: ownership here
   is the `## FILES` block and inventing a glob would silently re-own files.
3. Reproduce the `## FILES` block with EXACTLY the same set of paths. Keep
   the per-path comments if there are any. Adding or dropping a path changes
   which article owns which file, and a migration that re-owns files cannot
   be compared against the original.
   - If the source has NO `## FILES` block, do not create one.
   - Never promote a path mentioned in prose or in a STEPS example into
     FILES. A recipe that shows `functions/migrations/m00N_description.py`
     as a template is illustrating a filename, not claiming a file, and
     FILES is an ownership claim.
4. Keep every identifier, command and bug/incident number that appears in the
   source. Those are the load-bearing details.
5. Aim under 10000 tokens. Cut description, never cut a constraint.

Return the complete rewritten article as markdown, starting with `---`.
Return nothing else: no preamble, no code fence around the whole thing."""

FM = re.compile(r"\A---\n(.*?)\n---\n", re.S)


def frontmatter(text):
    m = FM.match(text)
    return m.group(1) if m else None


def field(fm, name):
    """Read one frontmatter field as raw text, list or scalar."""
    out, grab = [], False
    for line in (fm or "").splitlines():
        if re.match(rf"^{name}:", line):
            grab = True
            rest = line.split(":", 1)[1].strip()
            if rest:
                out.append(rest)
            continue
        if grab:
            if line.startswith(("  -", "\t-", "  ")):
                out.append(line.strip().lstrip("- ").strip())
                continue
            grab = False
    return out


PATH = re.compile(r"(?:^|[\s`|(])((?:lib|functions|admin|test|tools|scripts|"
                  r"integration_test|test_driver)/[\w./-]+\.\w+)")


def files_paths(text):
    """Every repo path named in the `## FILES` block.

    Ownership in this KB generation is the FILES block: there is no
    frontmatter and so no `covers:` glob. The block is written as a fenced
    `path  # comment` listing, so the paths are pulled by shape rather than by
    parsing a format that varies between articles.
    """
    out, grab = set(), False
    for line in text.splitlines():
        if line.startswith("## "):
            grab = line.strip().upper().startswith("## FILES")
            continue
        if grab:
            out.update(PATH.findall(line))
    return out


def trim_files_block(original, migrated):
    """Drop FILES lines naming a path the original did not own.

    Every persistent failure in this corpus was the same move: the rewrite
    gathers files mentioned in an INTERFACE or PATTERN section and lists them
    under FILES, which is an ownership claim rather than a mention. Three
    passes of asking the prompt not to did not converge, and it should not
    have to: ownership invariance is checkable, so it is enforced here rather
    than requested there.

    Only ADDITIONS are repairable. A migration that LOST a path has dropped
    an ownership claim, and inventing the line back is fabrication, so that
    still fails.
    """
    before = files_paths(original)
    # An original with no FILES block owns nothing, and the rewrite sometimes
    # builds one anyway out of paths it found in prose. Returning early here
    # let exactly that through to fail verification, which is how five
    # articles survived three retry passes: the repair declined to touch the
    # one case it most needed to.
    out, grab, changed = [], False, False
    for line in migrated.splitlines():
        if line.startswith("## "):
            grab = line.strip().upper().startswith("## FILES")
            out.append(line)
            continue
        if grab:
            found = set(PATH.findall(line))
            if found and not found <= before:
                changed = True
                continue
        out.append(line)
    return "\n".join(out) + ("\n" if migrated.endswith("\n") else "") \
        if changed else migrated


def verify(original, migrated, stem, atype):
    """Refuse a migration that changed identity or ownership.

    Ownership drives everything downstream, so a rewrite that quietly adds or
    drops a FILES path is not a reformatted article, it is a different one,
    and comparing it against the original measures the wrong thing.
    """
    fm = frontmatter(migrated)
    if not fm:
        return "no frontmatter in the result"
    if field(fm, "id") != [stem]:
        return f"id is {field(fm, 'id')}, want [{stem}]"
    if field(fm, "type") != [atype]:
        return f"type is {field(fm, 'type')}, want [{atype}]"
    if field(fm, "covers"):
        return "invented a covers: glob, which silently re-owns files"
    before, after = files_paths(original), files_paths(migrated)
    # 55 of 160 articles in this corpus have no FILES block at all, so an
    # empty-vs-empty comparison is a legitimate pass. An original that HAS the
    # section and yields no paths is a parser failure wearing the same face,
    # and it would wave through any rewrite at all.
    if "## FILES" in original and not before:
        return "could not parse the FILES block; ownership is unverifiable"
    if before != after:
        lost, gained = sorted(before - after), sorted(after - before)
        return (f"FILES ownership changed: lost {lost[:3]}, gained {gained[:3]}"
                f" ({len(lost)} lost, {len(gained)} gained)")
    body = migrated[len(FM.match(migrated).group(0)):]
    allowed = {"FILES", "INVARIANT", "GOTCHA", "DECISION", "VERIFY", "STEPS",
               "SEE ALSO", "COMMANDS"}
    infence = False
    for line in body.splitlines():
        if line.startswith("```"):
            infence = not infence
        if infence or not line.startswith("## "):
            continue
        name = line[3:].strip().split(":")[0].strip()
        if name not in allowed:
            return f"section outside the vocabulary survived: {name}"
    return None


def migrate_one(key, text, model, cache, stem, atype, tries=2):
    """Migrate one article, caching under `key`.

    The key is PASSED IN rather than recomputed. It used to be
    `sha1(text.encode())` here while both readers - the shrink report below and
    `apply_migration` in agent.py - looked the file up under
    `sha1(art.read_bytes())`. Those agree only for LF-only UTF-8 articles:
    `read_text()` translates universal newlines and decodes with the locale
    encoding, so one CRLF article is written under a name nothing reads. The
    run then reports "migrated N, failed 0", drops the article from the size
    stats, and every kb-mig cell raises MigrationMissing pointing at --root,
    which is not the cause. One derivation, used by all three sites.
    """
    dst = cache / f"{key}.md"
    if dst.exists():
        return dst.read_text(), None
    # Every exit from this loop has to be a value, never an exception. These
    # run under ex.map over ~160 articles, so anything that escapes kills the
    # whole migration and the articles after it are never attempted - the same
    # failure the previous commit fixed in judge.py. `last` starts bound for
    # the same reason: the JSONDecodeError path skips the assignment at the
    # bottom, so two bad replies used to raise UnboundLocalError here.
    last = "no usable reply"
    for _ in range(tries):
        try:
            r = subprocess.run(
                ["claude", "-p",
                 PROMPT.format(body=text[:400000], stem=stem, atype=atype),
                 "--output-format", "json", "--model", model,
                 "--disallowed-tools", "WebSearch", "WebFetch", "Bash", "Edit",
                 "Write", "Read"],
                capture_output=True, text=True, timeout=1200)
        except subprocess.TimeoutExpired:
            last = "timed out after 1200s"
            continue
        try:
            reply = json.loads(r.stdout)
        except json.JSONDecodeError:
            last = f"reply was not JSON (exit {r.returncode})"
            continue
        # A scalar or list reply has no .get, and AttributeError escaping here
        # aborts the run exactly like the decode error used to.
        if not isinstance(reply, dict):
            last = f"reply JSON was {type(reply).__name__}, not an object"
            continue
        # `.get("result", "")` only defaults when the KEY is absent, so a
        # `{"result": null}` reply returned None and `None.strip()` raised
        # AttributeError - out of the worker, through ex.map, killing the run.
        # That is the failure the isinstance check above exists to prevent,
        # one line below its own guard.
        out = reply.get("result")
        if not isinstance(out, str):
            last = f"reply result was {type(out).__name__}, not a string"
            continue
        out = out.strip()
        if out.startswith("```"):
            out = re.sub(r"\A```[a-z]*\n|\n```\Z", "", out)
        out = trim_files_block(text, out)
        why = verify(text, out, stem, atype)
        if why is None:
            dst.write_text(out)
            return out, None
        last = why
    return None, last


def main():
    ap = argparse.ArgumentParser()
    # Must match agent.py's --root default. They disagreed (work-vv2 here,
    # work-vv there), so the documented sequence with bare flags migrated one
    # corpus and benchmarked another and every kb-mig cell was refused - after
    # its none and kb siblings had already been paid for. agent.py, judge.py
    # and bench/README.md all name work-vv, so this is the one that moved.
    ap.add_argument("--root", default="bench/work-vv")
    ap.add_argument("--cache", default="bench/migrated")
    ap.add_argument("--model", default="sonnet")
    ap.add_argument("--jobs", type=int, default=6)
    a = ap.parse_args()

    root, cache = pathlib.Path(a.root), pathlib.Path(a.cache)
    cache.mkdir(parents=True, exist_ok=True)

    texts = {}
    for tree in sorted(root.iterdir()):
        for art in sorted((tree / "docs/kb").glob("*.md")):
            # INDEX.md is a routing table, not an article: no FILES block,
            # no claims, and apply_migration leaves it alone anyway. Feeding
            # it through costs a call per distinct version and produces
            # something the run will never read.
            if art.name in ("DIGEST.md", "INDEX.md"):
                continue
            stem = art.stem
            atype = stem.split("-")[0] if "-" in stem else "arch"
            # One read per file. `setdefault(k, art.read_text())` evaluated the
            # default eagerly, so every repeat of an already-seen article paid
            # a full read whose result was discarded - 399 instances over 169
            # distinct texts is 230 wasted reads.
            raw = art.read_bytes()
            key = hashlib.sha1(raw).hexdigest()
            if key not in texts:
                texts[key] = (raw.decode(), stem, atype)
    print(f"{len(texts)} unique article(s) to migrate, model={a.model}\n",
          flush=True)

    done = 0
    fails = []
    shrink = []

    def work(item):
        key, (text, stem, atype) = item
        out, why = migrate_one(key, text, a.model, cache, stem, atype)
        return key, text, out, why

    with cf.ThreadPoolExecutor(max_workers=a.jobs) as ex:
        for key, text, out, why in ex.map(work, texts.items()):
            if out is None:
                fails.append((key, why))
                print(f"  FAILED {key[:8]}: {why}", flush=True)
            else:
                done += 1
                # Measured from the string already in hand. Re-reading the
                # cache directory afterwards also counted files left over from
                # a previous --root that this run never touched.
                shrink.append(len(out) / max(1, len(text)))
                if done % 20 == 0:
                    print(f"  {done}/{len(texts)}", flush=True)

    failed = len(fails)
    print(f"\nmigrated {done}, failed {failed}")
    if shrink:
        shrink.sort()
        print(f"  size vs original: median {shrink[len(shrink)//2]:.2f}x, "
              f"min {shrink[0]:.2f}x, max {shrink[-1]:.2f}x")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
