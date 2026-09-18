#!/bin/sh
# Emit `<test file>\t<production file>` for a Rust workspace.
#
# READ THIS BEFORE CONFIGURING IT. Rust's dominant test convention is a
# `#[cfg(test)] mod tests` block INSIDE the file it tests. That produces no
# cross-file edge, because there are no two files — and no link command can
# invent one. This script therefore only ever reports edges out of `tests/`,
# the integration-test directory.
#
# A crate whose tests are all inline will produce NOTHING here, and kb treats a
# silently empty link command as a failure, correctly: it cannot tell that from
# a broken toolchain. If that is your repo, do not configure this command at
# all. An empty tests column is the honest answer, and `stem_rules` already
# recovers the `foo.rs` / `tests/foo.rs` pairing without pretending to have
# read an import graph.
#
# Module paths are resolved by walking `mod` declarations from each crate root,
# rather than by mapping `a::b::c` onto `src/a/b/c.rs`. The two agree in a
# conventional crate and diverge in exactly the ones worth getting right: a
# module declared in `lib.rs` lives beside it, while one declared inside
# `src/foo.rs` lives under `src/foo/`, and `mod.rs` means the directory itself.
#
# Usage in .kb/config.yaml:
#
#   link_commands:
#     - name: rust
#       command: [kb, imports, rust]
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

BLOCK = re.compile(r"/\*.*?\*/", re.S)
LINE = re.compile(r"//[^\n]*")
# `mod foo;` declares a child module in another file. `mod foo { … }` is inline
# and has no file, so the trailing semicolon is required.
MOD = re.compile(r"^\s*(?:pub(?:\([^)]*\))?\s+)?mod\s+([A-Za-z_]\w*)\s*;", re.M)
# Cargo's own table syntax is enough here: only [package] name matters.
CARGO_NAME = re.compile(r"^\s*name\s*=\s*\"([^\"]+)\"", re.M)
CARGO_PKG = re.compile(r"^\s*\[package\]\s*$", re.M)


def read(p):
    try:
        return LINE.sub("", BLOCK.sub("", open(p, encoding="utf-8", errors="replace").read()))
    except OSError as e:
        print(f"{p}: {e}", file=sys.stderr)
        return ""


def crate_name(cargo):
    try:
        text = open(cargo, encoding="utf-8", errors="replace").read()
    except OSError:
        return None
    m = CARGO_PKG.search(text)
    if not m:
        return None  # a virtual workspace manifest declares no package
    n = CARGO_NAME.search(text, m.end())
    return n.group(1) if n else None


# `use my_crate::…` spells the crate name with underscores even when Cargo.toml
# spells it with hyphens. Getting this wrong drops every edge in any crate with
# a hyphenated name, which is most published ones.
crates = {}  # module-path crate name -> crate root directory
for p in tracked:
    if os.path.basename(p) != "Cargo.toml":
        continue
    name = crate_name(p)
    if name:
        crates[name.replace("-", "_")] = os.path.dirname(p)


def walk_modules(root_file, base_prefix):
    """module path (tuple) -> file, by following `mod x;` from a crate root."""
    found = {}
    # Each entry: (file, module path, directory its children live in).
    stack = [(root_file, (), os.path.dirname(root_file))]
    seen = {root_file}
    while stack:
        path, mod_path, childdir = stack.pop()
        found[mod_path] = path
        for name in MOD.findall(read(path)):
            flat = os.path.join(childdir, f"{name}.rs")
            nested = os.path.join(childdir, name, "mod.rs")
            if flat in trackset:
                child, cdir = flat, os.path.join(childdir, name)
            elif nested in trackset:
                child, cdir = nested, os.path.join(childdir, name)
            else:
                continue
            if child in seen:
                continue
            seen.add(child)
            stack.append((child, mod_path + (name,), cdir))
    return {base_prefix + k: v for k, v in found.items()}


modules = {}  # ("my_crate", "a", "b") -> src/a/b.rs
for name, root in crates.items():
    for entry in ("src/lib.rs", "src/main.rs"):
        f = os.path.join(root, entry) if root else entry
        f = os.path.normpath(f)
        if f in trackset:
            modules.update(walk_modules(f, (name,)))

# `use` supports nesting and globs: `use a::{b::C, d as e, f::*};`. Rather than
# parse that grammar, take every `::`-joined path-like run out of the file and
# resolve the longest prefix that is a known module. A name that is not a
# module resolves to nothing, so over-collecting here costs nothing.
PATHS = re.compile(r"\b([A-Za-z_]\w*(?:\s*::\s*[A-Za-z_]\w*)+)")


def referenced_modules(text):
    for m in PATHS.finditer(text):
        parts = tuple(s.strip() for s in m.group(1).split("::"))
        # Down to length 1, which is the crate root itself: `use helper::aid`
        # names a function defined in `helper/src/lib.rs`, and stopping at two
        # segments missed every edge into a crate with a flat public API.
        while parts:
            if parts in modules:
                yield modules[parts]
                break
            parts = parts[:-1]


def test_files():
    for p in tracked:
        if not p.endswith(".rs"):
            continue
        segs = p.split("/")
        # `tests/` directly under a crate root, not a directory called tests
        # somewhere in the source tree.
        for root in crates.values():
            prefix = (root + "/tests/") if root else "tests/"
            if p.startswith(prefix):
                yield p
                break
        else:
            if segs[0] == "tests":
                yield p


tests = sorted(set(test_files()))
if not tests:
    print(
        "no integration tests found under any crate's tests/ directory. Rust "
        "unit tests live in `#[cfg(test)] mod tests` inside the file they "
        "test, which is not a cross-file edge — see the header of this "
        "script, and consider not configuring it for this repo.",
        file=sys.stderr,
    )

edges = set()
for p in tests:
    for target in referenced_modules(read(p)):
        if target != p and target in trackset and target not in tests:
            edges.add((p, target))

for test, src in sorted(edges):
    print(f"{test}\t{src}")
PY
