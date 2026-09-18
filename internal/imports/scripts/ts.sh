#!/bin/sh
# Emit `<test file>\t<production file>` for a TypeScript or JavaScript repo.
#
# madge and dependency-cruiser both do this better, and both need node_modules
# installed. That makes the answer depend on whether someone has run `npm ci`,
# so the committed reverse index would differ between a fresh clone and a
# working machine — the class of failure this whole tool exists to refuse. This
# reads the committed source and nothing else.
#
# Only FIRST-PARTY edges. A bare specifier like `react` resolves into
# node_modules, which is not this repo's source and not something an article
# can own. Relative specifiers and tsconfig `paths` aliases are resolved; every
# other bare specifier is dropped rather than guessed at.
#
# Resolution follows the parts of Node's algorithm that matter here: an
# extensionless specifier tries each extension in order, then `/index.*`. A
# specifier ending in `.js` is also tried as `.ts` and `.tsx`, because that is
# what NodeNext-style TypeScript emits and it is otherwise a dead end.
#
# Assumes test_rules classify `*.test.*`, `*.spec.*` and files under
# `__tests__/`, `test/`, `tests/`, `e2e/` or `cypress/` as tests. kb fails
# loudly if its config disagrees.
#
# Usage in .kb/config.yaml:
#
#   link_commands:
#     - name: typescript
#       command: [kb, imports, typescript]
set -eu

python3 - <<'PY'
import json
import os
import re
import subprocess
import sys

EXTS = (".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts")
# Order matters: a repo with both `x.ts` and `x.js` (a build artifact someone
# committed) means the TypeScript one, because that is what the other source
# files are written against.
TRY = (".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs")

tracked = [
    p for p in subprocess.run(
        ["git", "ls-files", "-z"], stdout=subprocess.PIPE, check=True
    ).stdout.decode().split("\0") if p
]
trackset = set(tracked)
src_files = [p for p in tracked if p.endswith(EXTS)]

TEST_DIRS = {"__tests__", "test", "tests", "e2e", "cypress", "__mocks__"}


def is_test(p):
    base = os.path.basename(p)
    stem = base.rsplit(".", 1)[0]
    if stem.endswith((".test", ".spec")) or ".test." in base or ".spec." in base:
        return True
    return bool(TEST_DIRS.intersection(p.split("/")[:-1]))


# Comments are stripped before the specifier scan. A commented-out import is
# not an edge, and a URL in a line comment matched the require() pattern often
# enough to put a phantom path in the index.
BLOCK = re.compile(r"/\*.*?\*/", re.S)
LINE = re.compile(r"(?<![:\\])//[^\n]*")

SPEC = re.compile(
    r"""(?:
          \bfrom\s*['"]([^'"]+)['"]          # import x from 'y' / export … from
        | \bimport\s*['"]([^'"]+)['"]        # import 'y' (side effect)
        | \bimport\s*\(\s*['"]([^'"]+)['"]   # dynamic import('y')
        | \brequire\s*\(\s*['"]([^'"]+)['"]  # require('y')
        )""",
    re.X,
)


def specifiers(text):
    text = LINE.sub("", BLOCK.sub("", text))
    for m in SPEC.finditer(text):
        yield next(g for g in m.groups() if g is not None)


def candidates(base):
    """Every file a specifier resolving to `base` could mean."""
    yield base
    # `./util.js` in a NodeNext project means `./util.ts` on disk.
    for e in (".js", ".mjs", ".cjs"):
        if base.endswith(e):
            stem = base[: -len(e)]
            for t in (".ts", ".tsx", ".mts", ".cts"):
                yield stem + t
    for e in TRY:
        yield base + e
    for e in TRY:
        yield f"{base}/index{e}"


def norm(p):
    # os.path.normpath collapses `a/../b`, and leaves no leading `./`.
    p = os.path.normpath(p)
    return "" if p == "." else p


# tsconfig `paths` is how a monorepo spells a first-party import that is not
# relative. Ignoring it drops every `@app/thing` edge, which in a repo that
# uses aliases throughout is most of them.
def load_jsonc(path):
    try:
        raw = open(path, encoding="utf-8", errors="replace").read()
    except OSError:
        return None
    # tsconfig is JSON with comments, and often a trailing comma.
    raw = LINE.sub("", BLOCK.sub("", raw))
    raw = re.sub(r",(\s*[}\]])", r"\1", raw)
    try:
        return json.loads(raw)
    except json.JSONDecodeError as e:
        print(f"{path}: {e}", file=sys.stderr)
        return None


aliases = []  # (prefix, [replacement dirs]), longest prefix first
for cfg in [p for p in tracked if os.path.basename(p) in
            ("tsconfig.json", "tsconfig.base.json", "jsconfig.json")]:
    data = load_jsonc(cfg)
    if not data:
        continue
    opts = data.get("compilerOptions") or {}
    root = os.path.dirname(cfg)
    base_url = norm(os.path.join(root, opts.get("baseUrl", ".")))
    for pattern, targets in (opts.get("paths") or {}).items():
        prefix = pattern[:-1] if pattern.endswith("*") else pattern
        subs = [
            norm(os.path.join(base_url, t[:-1] if t.endswith("*") else t))
            for t in targets
        ]
        aliases.append((prefix, subs))
aliases.sort(key=lambda a: -len(a[0]))


def resolve(spec, importer):
    bases = []
    if spec.startswith("."):
        bases.append(norm(os.path.join(os.path.dirname(importer), spec)))
    else:
        for prefix, subs in aliases:
            if spec.startswith(prefix):
                rest = spec[len(prefix):]
                bases.extend(norm(os.path.join(s, rest)) for s in subs)
                break
        else:
            return None  # a bare specifier: node_modules, not this repo
    for base in bases:
        if not base:
            continue
        for cand in candidates(base):
            if cand in trackset:
                return cand
    return None


edges = set()
for p in src_files:
    if not is_test(p):
        continue
    try:
        text = open(p, encoding="utf-8", errors="replace").read()
    except OSError as e:
        print(f"{p}: {e}", file=sys.stderr)
        continue
    for spec in specifiers(text):
        target = resolve(spec, p)
        if target and target != p and not is_test(target):
            edges.add((p, target))

for test, src in sorted(edges):
    print(f"{test}\t{src}")
PY
