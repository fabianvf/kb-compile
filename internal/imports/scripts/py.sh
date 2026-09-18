#!/bin/sh
# Emit `<test file>\t<production file>` for a Python repo, by parsing imports.
#
# There is no `go list` for Python. pydeps and importlab both need the package
# installed and importable, which makes the answer depend on the state of a
# virtualenv rather than on the contents of the repo — the gate would then pass
# on one machine and fail on another. This walks the source instead, so the
# answer is a function of the committed files and nothing else.
#
# The parse is `ast`, not a regex. `import` inside a try/except, a conditional
# import guarded by TYPE_CHECKING, a multi-line parenthesised `from x import
# (a, b)` — all of them are ordinary syntax and all of them defeat line-based
# matching in exactly the cases nobody checks.
#
# Python imports a MODULE, so edges are file-level rather than package-level:
# `from app.core.engine import run` links to `app/core/engine.py` alone, not to
# everything beside it. That is finer than the Go equivalent can be, and it is
# the whole reason to prefer real resolution over a directory heuristic.
#
# Assumes test_rules classify `test_*.py`, `*_test.py` and files under a
# `tests/` directory as tests. kb fails loudly if its config disagrees, so a
# mismatch is visible rather than silently halving the index.
#
# Usage in .kb/config.yaml:
#
#   link_commands:
#     - name: python
#       command: [kb, imports, python]
set -eu

python3 - <<'PY'
import ast
import os
import subprocess
import sys

# git decides what the repo contains, here as everywhere in this tool. A
# filesystem walk would pick up a stale .pyc directory or an untracked scratch
# file and make the committed index differ between machines.
tracked = [
    p for p in subprocess.run(
        ["git", "ls-files", "-z"], stdout=subprocess.PIPE, check=True
    ).stdout.decode().split("\0") if p
]
py = [p for p in tracked if p.endswith(".py")]
trackset = set(tracked)


def is_test(p):
    base = os.path.basename(p)
    parts = p.split("/")[:-1]
    return (
        base.startswith("test_")
        or base[:-3].endswith("_test")
        or "tests" in parts
        or "test" in parts
    )


# A source ROOT is the directory an import path is relative to. Walk up from
# each file while `__init__.py` is present: the first directory without one is
# where the dotted name starts. A repo laid out as `src/app/core/engine.py`
# with `src/app/__init__.py` therefore yields `app.core.engine`, which is what
# the code actually writes — guessing `src.app.core.engine` resolves nothing.
pkgdirs = {os.path.dirname(p) for p in py if os.path.basename(p) == "__init__.py"}


def module_names(path):
    """Every dotted name this file can be imported as."""
    d, base = os.path.split(path)
    stem = base[:-3]
    parts = [] if stem == "__init__" else [stem]
    while d and d in pkgdirs:
        d, name = os.path.split(d)
        parts.insert(0, name)
    names = {".".join(parts)} if parts else set()
    # Also the name relative to the repo root, for a flat layout with no
    # __init__.py at all, which is normal for scripts and for pytest's
    # rootdir-relative imports.
    flat = path[:-3].replace("/", ".")
    names.add(flat)
    if flat.endswith(".__init__"):
        names.add(flat[: -len(".__init__")])
    return {n for n in names if n}


# dotted name -> the file that defines it. A name can legitimately be produced
# by two files under different roots; first tracked path wins, deterministically,
# because `py` is derived from git's sorted output.
modules = {}
for p in py:
    for n in module_names(p):
        modules.setdefault(n, p)


def resolve(name):
    """Longest dotted prefix of `name` that is a first-party module."""
    parts = name.split(".")
    while parts:
        hit = modules.get(".".join(parts))
        if hit:
            return hit
        parts.pop()
    return None


def import_groups(path, src):
    """Per import statement: (preferred names, fallback name).

    `from pkg import mod` and `from pkg import Thing` are spelled identically,
    and mean different files: the first names a MODULE, the second an
    attribute defined in `pkg/__init__.py`. So each statement offers its
    submodule candidates first and falls back to the package only when none
    of them turns out to be a real module. Emitting both put an edge to an
    `__init__.py` — usually an empty one — on every `from pkg import mod` in
    the repo, and an edge is a file an agent opens.
    """
    try:
        tree = ast.parse(src, filename=path)
    except SyntaxError as e:
        # A file that does not parse is a fact worth reporting: staying silent
        # would drop its edges and read as "this test imports nothing".
        print(f"{path}: {e}", file=sys.stderr)
        return []
    here = os.path.dirname(path)
    out = []
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            out.extend(([a.name], None) for a in node.names)
        elif isinstance(node, ast.ImportFrom):
            if node.level:
                # `from ..pkg import x`: climb `level` directories from here,
                # then read the result back as a dotted name.
                base = here
                for _ in range(node.level - 1):
                    base = os.path.dirname(base)
                prefix = base.replace("/", ".") if base else ""
                mod = f"{prefix}.{node.module}" if node.module else prefix
            else:
                mod = node.module or ""
            if not mod:
                continue
            out.append(
                ([f"{mod}.{a.name}" for a in node.names if a.name != "*"], mod)
            )
    return out


edges = set()
for p in py:
    if not is_test(p):
        continue
    try:
        src = open(p, encoding="utf-8", errors="replace").read()
    except OSError as e:
        print(f"{p}: {e}", file=sys.stderr)
        continue
    for preferred, fallback in import_groups(p, src):
        hits = [t for t in (resolve(n) for n in preferred) if t]
        if not hits and fallback:
            if t := resolve(fallback):
                hits = [t]
        for target in hits:
            if target != p and not is_test(target):
                edges.add((p, target))

for test, src in sorted(edges):
    if test in trackset and src in trackset:
        print(f"{test}\t{src}")
PY
