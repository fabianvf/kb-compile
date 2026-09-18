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
# Usage in .kb/config.yaml:
#
#   link_commands:
#     - name: go
#       command: [kb, imports, go]
set -eu

# The first module only. In a go.work workspace `go list -m` prints one per
# line, and a multi-line value matched no import path at all, so the command
# exited 0 having emitted nothing — which reads as "this package has no tests".
MODULE=$(go list -m | head -n 1)

# Captured rather than piped so `set -eu` sees a `go list` failure. /bin/sh has
# no pipefail, so in a pipeline a broken module was silently an empty result.
# stderr is left alone: kb reports a failed link command, and the reason for
# the failure is the useful half of that.
LIST=$(go list -e -json ./...)

printf '%s' "$LIST" | python3 -c '
import json, os, subprocess, sys

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

# GoFiles holds only the files that build for the CURRENT GOOS/GOARCH, so a
# package with `x_linux.go` and `x_darwin.go` yields a different answer on each
# machine. The reverse index is compared byte-for-byte against a regeneration,
# so that is a committed artifact CI rejects and the author cannot reproduce -
# green locally, red in CI, which is the failure this whole design avoids.
# IgnoredGoFiles holds what the build constraints excluded; union them.
SRC_KEYS = ("GoFiles", "CgoFiles", "IgnoredGoFiles")
TEST_KEYS = ("TestGoFiles", "XTestGoFiles", "IgnoredGoFiles")


def rel(p):
    d = os.path.relpath(p.get("Dir") or os.getcwd(), os.getcwd())
    return "" if d == "." else d + "/"


def names(p, keys):
    seen = []
    for k in keys:
        for f in p.get(k, []):
            if f not in seen:
                seen.append(f)
    return seen


# import path -> its non-test source files, repo-root-relative
files = {}
for p in pkgs:
    d = rel(p)
    src = [f for f in names(p, SRC_KEYS) if not f.endswith("_test.go")]
    files[p["ImportPath"]] = [d + f for f in src]

edges = set()
for p in pkgs:
    d = rel(p)
    tests = [d + f for f in names(p, TEST_KEYS) if f.endswith("_test.go")]
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

# `go list` walks the FILESYSTEM; kb validates every path against `git
# ls-files`. Without this filter one untracked scratch test file made kb print
# "which git does not track. Paths must be repo-root-relative" once per edge -
# advice that is wrong twice, since the path is relative and the fix is `git
# add`. git decides what exists, here as everywhere else.
tracked = set(
    subprocess.run(
        ["git", "ls-files", "-z"], stdout=subprocess.PIPE, check=True
    ).stdout.decode().split("\0")
)

for test, src in sorted(edges):
    if test in tracked and src in tracked:
        print(f"{test}\t{src}")
' "$MODULE"
