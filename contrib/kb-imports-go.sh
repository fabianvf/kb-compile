#!/bin/sh
# Emit `<test file>\t<production file>` for a Go module, using `go list`.
#
# This is what the command strategy is for: `go list` already knows the import
# graph exactly, including build tags, aliased imports and generated code. A
# regex over import lines approximates that and is wrong in precisely the cases
# that are hardest to notice.
#
# Every _test.go file in a package is linked to every non-test .go file in the
# packages that package imports, plus its own. Go tests import a PACKAGE, so
# there is no finer-grained truth available: claiming one file would be a
# guess.
#
# Usage in .kb/config.json:
#
#   "link_commands": [
#     { "name": "go", "command": ["contrib/kb-imports-go.sh"] }
#   ]
set -eu

MODULE=$(go list -m)

go list -e -json ./... 2>/dev/null | python3 -c '
import json, os, sys

module = sys.argv[1]
decoder = json.JSONDecoder()
raw = sys.stdin.read()
pkgs, i = [], 0
while i < len(raw):
    while i < len(raw) and raw[i].isspace():
        i += 1
    if i >= len(raw):
        break
    obj, i = decoder.raw_decode(raw, i)
    pkgs.append(obj)

# import path -> its non-test source files, repo-root-relative
files = {}
for p in pkgs:
    d = os.path.relpath(p.get("Dir", ""), os.getcwd())
    d = "" if d == "." else d + "/"
    files[p["ImportPath"]] = [d + f for f in p.get("GoFiles", [])]

edges = set()
for p in pkgs:
    d = os.path.relpath(p.get("Dir", ""), os.getcwd())
    d = "" if d == "." else d + "/"
    tests = [d + f for f in p.get("TestGoFiles", []) + p.get("XTestGoFiles", [])]
    if not tests:
        continue
    # The package under test, plus everything it and its tests import. Only
    # first-party packages: a link to a vendored dependency is noise.
    targets = {p["ImportPath"]}
    for key in ("Imports", "TestImports", "XTestImports"):
        targets.update(p.get(key, []))
    for t in targets:
        if t != module and not t.startswith(module + "/"):
            continue
        for src in files.get(t, []):
            for test in tests:
                if test != src:
                    edges.add((test, src))

for test, src in sorted(edges):
    print(f"{test}\t{src}")
' "$MODULE"
