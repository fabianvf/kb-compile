#!/bin/sh
# Emit `<test file>\t<production file>` for a Dart or Flutter repo.
#
# `dart pub deps` describes the dependency tree, not the file graph, and
# resolving it needs `pub get` to have run. The file graph is entirely in the
# committed source: Dart's import forms are unambiguous and there is no
# extension or index-file guessing to do, which makes this one of the more
# exact scripts here.
#
# Every pubspec in the tree is read, not just the root one, so a melos-style
# workspace resolves `package:` imports that cross package boundaries. Without
# that, every edge in a multi-package repo silently disappears — and a
# multi-package repo is the normal shape for a Flutter app with shared code.
#
# `part` directives are followed. A part file is a continuation of the library
# that declares it, so a test exercising the library exercises its parts, and a
# test naming a part directly is still naming this repo's source.
#
# Assumes test_rules classify `*_test.dart` and anything under `test/` or
# `integration_test/` as tests. kb fails loudly if its config disagrees.
#
# Usage in .kb/config.yaml:
#
#   link_commands:
#     - name: dart
#       command: [kb, imports, dart]
set -eu

python3 - <<'PY'
import os
import re
import subprocess
import sys

tracked = [
    p for p in subprocess.run(
        ["git", "ls-files", "-z"], stdout=subprocess.PIPE, check=True
    ).stdout.decode().split("\0") if p
]
trackset = set(tracked)
dart = [p for p in tracked if p.endswith(".dart")]


def is_test(p):
    parts = p.split("/")
    return (
        os.path.basename(p).endswith("_test.dart")
        or "test" in parts[:-1]
        or "integration_test" in parts[:-1]
    )


BLOCK = re.compile(r"/\*.*?\*/", re.S)
LINE = re.compile(r"//[^\n]*")
# import / export / part all take a single quoted URI. `part of` does not take
# a path in modern Dart, and is excluded by requiring the quote.
DIRECTIVE = re.compile(
    r"""^\s*(import|export|part)\s+(?:r?)['"]([^'"]+)['"]""", re.M
)
PKG_NAME = re.compile(r"^name:\s*([A-Za-z_][\w]*)\s*$", re.M)

# package name -> the directory holding its pubspec. `package:foo/bar.dart`
# means `<that dir>/lib/bar.dart`, which is the one rule the whole `package:`
# form reduces to.
packages = {}
for p in tracked:
    if os.path.basename(p) != "pubspec.yaml":
        continue
    try:
        m = PKG_NAME.search(open(p, encoding="utf-8", errors="replace").read())
    except OSError as e:
        print(f"{p}: {e}", file=sys.stderr)
        continue
    if m:
        packages[m.group(1)] = os.path.dirname(p)

if not packages:
    print("no pubspec.yaml with a `name:` found; package: imports cannot be "
          "resolved", file=sys.stderr)


def norm(p):
    p = os.path.normpath(p)
    return "" if p == "." else p


def resolve(uri, importer):
    if uri.startswith("dart:"):
        return None
    if uri.startswith("package:"):
        rest = uri[len("package:"):]
        pkg, _, path = rest.partition("/")
        root = packages.get(pkg)
        if root is None:
            return None  # a third-party package, not this repo
        cand = norm(os.path.join(root, "lib", path))
        return cand if cand in trackset else None
    if uri.startswith(("http:", "https:")):
        return None
    cand = norm(os.path.join(os.path.dirname(importer), uri))
    return cand if cand in trackset else None


def directives(path):
    try:
        text = open(path, encoding="utf-8", errors="replace").read()
    except OSError as e:
        print(f"{path}: {e}", file=sys.stderr)
        return []
    return DIRECTIVE.findall(LINE.sub("", BLOCK.sub("", text)))


def uris(path, kinds=("import", "export", "part")):
    return [u for kind, u in directives(path) if kind in kinds]


# A library's parts are part of it, so an edge to the library is an edge to
# each part as well. ONLY `part` — an earlier version followed every directive
# here, so an edge to a library dragged in everything that library imported,
# one level deep. That is not what a test touched, and on a Flutter app it
# multiplied the index by an order of magnitude while reading as thoroughness.
parts = {}
for p in dart:
    if is_test(p):
        continue
    found = []
    for uri in uris(p, kinds=("part",)):
        t = resolve(uri, p)
        if t and t != p and not is_test(t):
            found.append(t)
    parts[p] = found

edges = set()
for p in dart:
    if not is_test(p):
        continue
    for uri in uris(p):
        target = resolve(uri, p)
        if not target or target == p or is_test(target):
            continue
        edges.add((p, target))
        for sub in parts.get(target, []):
            if sub != p:
                edges.add((p, sub))

for test, src in sorted(edges):
    print(f"{test}\t{src}")
PY
