#!/bin/sh
# Emit `<test file>\t<production file>` for a Java repo, from package and
# import declarations.
#
# Deliberately does not shell out to Maven or Gradle. Both know the real
# answer, both need a resolved dependency tree to produce it, and both can take
# minutes — from a commit hook, on every commit. Worse, the answer would then
# depend on what is in the local artifact cache. Every fact used here is in the
# committed source.
#
# Three kinds of edge, because Java tests reach their subject three ways:
#
#   1. An explicit `import a.b.C;` — exact, and the common case.
#   2. A wildcard `import a.b.*;` — the package is named but the class is not,
#      so a class from that package counts only if its simple name appears in
#      the test body.
#   3. No import at all, because the test is in the SAME package as its
#      subject. This is the convention Maven's `src/test/java` layout exists to
#      support, so skipping it would miss the majority of edges in a
#      conventionally laid out repo.
#
# Cases 2 and 3 match a simple name against the test's text, which is an
# approximation. It is bounded by only ever considering classes that are
# already visible to that file under Java's own scoping rules, and comments and
# string literals are stripped first so a class named in prose is not an edge.
#
# Assumes test_rules classify `*Test.java`, `*Tests.java`, `*IT.java`,
# `Test*.java` and anything under `src/test/` as tests. kb fails loudly if its
# config disagrees.
#
# Usage in .kb/config.yaml:
#
#   link_commands:
#     - name: java
#       command: [kb, imports, java]
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
java = [p for p in tracked if p.endswith(".java")]


def is_test(p):
    base = os.path.basename(p)[:-5]
    if "/src/test/" in f"/{p}" or p.startswith("src/test/"):
        return True
    return (
        base.endswith(("Test", "Tests", "IT", "ITCase", "TestCase"))
        or base.startswith("Test")
    )


BLOCK = re.compile(r"/\*.*?\*/", re.S)
LINE = re.compile(r"//[^\n]*")
STRING = re.compile(r'"(?:\\.|[^"\\])*"')
PACKAGE = re.compile(r"^\s*package\s+([\w.]+)\s*;", re.M)
IMPORT = re.compile(r"^\s*import\s+(static\s+)?([\w.]+)(\.\*)?\s*;", re.M)


def strip(text):
    """Remove comments and string literals before any name matching."""
    return STRING.sub('""', LINE.sub("", BLOCK.sub("", text)))


sources = {}  # path -> (package, simple name, stripped body)
for p in java:
    try:
        raw = open(p, encoding="utf-8", errors="replace").read()
    except OSError as e:
        print(f"{p}: {e}", file=sys.stderr)
        continue
    body = strip(raw)
    m = PACKAGE.search(body)
    # A file with no package declaration is in the default package. Legal, and
    # it still has a simple name to match on.
    sources[p] = (m.group(1) if m else "", os.path.basename(p)[:-5], body)

# Fully-qualified name -> file, and package -> its files. The FQN is derived
# from the DECLARED package plus the filename rather than from the directory,
# because a repo whose directories disagree with its packages still compiles
# from the declaration, and that is the truth the imports are written against.
by_fqn = {}
by_pkg = {}
for p, (pkg, name, _) in sources.items():
    if is_test(p):
        continue
    fqn = f"{pkg}.{name}" if pkg else name
    by_fqn.setdefault(fqn, p)
    by_pkg.setdefault(pkg, []).append(p)


def word_in(name, body):
    return re.search(rf"\b{re.escape(name)}\b", body) is not None


edges = set()
for p, (pkg, _, body) in sources.items():
    if not is_test(p):
        continue
    targets = set()

    for m in IMPORT.finditer(body):
        static, path, wild = m.groups()
        if wild:
            # `import a.b.*;` names the package only.
            for cand in by_pkg.get(path, []):
                if word_in(sources[cand][1], body):
                    targets.add(cand)
            continue
        # `import static a.b.C.method;` — the class is the second-to-last
        # segment. Try the whole path first anyway: a static import of a
        # nested class ends at the class, not at a method.
        if path in by_fqn:
            targets.add(by_fqn[path])
        elif static and "." in path:
            owner = path.rsplit(".", 1)[0]
            if owner in by_fqn:
                targets.add(by_fqn[owner])

    # Same package, no import required.
    for cand in by_pkg.get(pkg, []):
        if cand != p and word_in(sources[cand][1], body):
            targets.add(cand)

    for t in targets:
        if t != p:
            edges.add((p, t))

for test, src in sorted(edges):
    print(f"{test}\t{src}")
PY
