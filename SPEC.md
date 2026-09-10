# The KB format

What an article is, what goes in it, and which rules are mechanically enforced.

This is the reference the `kb-*` skills work from. `kb graph` and `kb fresh`
enforce the parts that can be enforced; the rest is here because it is the
difference between a KB worth reading and a second copy of the code.

---

## 1. What this is for

An LLM knowledge base is **the context that is not in the code**.

An agent can read the code. What it cannot read is why the code is shaped that
way, which two files must change together, which plausible-looking edit has
been tried and broke production, and which apparent redundancy is load-bearing.

That is the entire content brief. An article that describes what the code does
is not neutral - it is *negative* value: it costs a compile every time the code
moves, and it displaces the paragraph that would have said something useful.

**The test for any sentence in an article:** could a competent agent have
derived this by reading the file? If yes, delete it.

Corollary, and the failure mode to watch for hardest: a generated KB full of
fluent architectural prose scores well on every impression and helps nobody.
Fluency is not the bar. Non-derivability is.

---

## 2. Article anatomy

One file per subsystem, `<type>-<slug>.md`, in the KB directory.

```markdown
---
id: arch-scoring
type: arch
covers:
  - app/scoring/**
gated_by:
  - policy-data-retention
see_also:
  - arch-api
---
# Scoring pipeline

One paragraph on what this subsystem is and where it sits. Orientation, not
description.

## FILES

...

## INVARIANT

...

## SEE ALSO

- [arch-api.md](arch-api.md) - owns the wire shape scores land in, so a
  change to the return type is a change to that contract.
```

### Frontmatter

| Field | Required | Meaning |
|---|---|---|
| `id` | yes | Must equal the filename without `.md`. Enforced. |
| `type` | only with a taxonomy | Must match the filename prefix per `article_types`. Enforced only when `article_types` is configured. |
| `covers` | no | Globs of paths this article OWNS. Every glob must match at least one tracked file. Enforced. |
| `gated_by` | no | Articles that must be read FIRST. Must resolve. Enforced. |
| `see_also` | no | Outbound edges. Must resolve; may not be self. Enforced. |

No other keys are permitted - an unknown key fails, because a typo'd
`covers:` silently owns nothing.

`covers:` supports two glob forms: `dir/**` (anything beneath `dir`) and a
single `*` that does not cross `/`. There is no third form; if you need one,
list the paths in `## FILES` instead.

### Section vocabulary

Fixed. `FILES`, `INVARIANT`, `GOTCHA`, `DECISION`, `VERIFY`, `STEPS`,
`SEE ALSO`, `COMMANDS`. Not every article needs all of them.

The vocabulary is fixed so a reader knows where to look without reading the
whole article, and so "is there a decision recorded here?" is a scan rather
than a comprehension task.

| Section | Holds | The test it must pass |
|---|---|---|
| `FILES` | The ownership claim | - |
| `INVARIANT` | Something that must stay true | Name what BREAKS if it stops being true |
| `GOTCHA` | A trap that has actually been hit | Cite the incident, bug, or commit |
| `DECISION` | A choice made and why | Name the alternative that was rejected |
| `VERIFY` | How to check this still holds | Must be a runnable command or a named test |
| `STEPS` | An ordered procedure (recipes) | Each step independently checkable |
| `SEE ALSO` | Outbound edges | See §4 |
| `COMMANDS` | Commands specific to this subsystem | - |

An `INVARIANT` that names no consequence is a description wearing a heading.
A `DECISION` with no rejected alternative is not a decision, it is a fact
about the current code - which the code already states, better.

---

## 3. Ownership

`## FILES` is the explicit ownership claim: a path listed there is a path this
article is responsible for keeping accurate.

**Two shapes, both count.** A fenced block of bare paths:

````markdown
## FILES

```
app/scoring/engine.py
app/scoring/windows.py
```
````

...or a markdown table with backticked paths:

```markdown
## FILES

| Path | Role |
|---|---|
| `app/scoring/engine.py` | Entry point |
| `app/scoring/windows.py` | Period boundaries |
```

A path in a table cell must be **repo-root-relative**. A bare filename in a
cell (`engine.py`) is shorthand for the reader and is deliberately *not* an
ownership claim - it cannot be resolved unambiguously, so treating it as one
would let an article claim a file that does not exist.

Every path an article names anywhere - in `## FILES` or in ordinary prose -
must resolve against `git ls-files`. Not against the filesystem: a build
artifact sitting in a working tree would make the check pass on one machine
and fail on another, and a gate whose verdict depends on the machine is worse
than no gate.

Two escape hatches, both in `.kb/config.yaml`, both requiring a written reason:

- `not_repo_paths` - looks like a path, genuinely cannot be one (a cloud
  storage key, an illustrative URL)
- `generated_paths` - real and correct, but generated and gitignored

If you are adding a third entry to either in one sitting, the rule is probably
wrong rather than the paths.

---

## 4. Edges

A KB with no edges is a pile of documents with an index. The index becomes the
only route between any two articles, which is a star topology - every journey
goes through the hub, and nothing tells a reader that finishing *this* article
obliges them to read *that* one.

Two edge types:

- **`gated_by:`** - read that first. Use for genuine prerequisites: a policy
  the article's subject must satisfy, a data model it assumes.
- **`see_also:`** / `## SEE ALSO` - read that next, and here is why.

**An edge must name an obligation, not a topic.**

Weak:

```markdown
- [arch-api.md](arch-api.md) - related to the API layer.
```

Better:

```markdown
- [arch-api.md](arch-api.md) - owns the wire shape scores land in, so a change
  to the return type is a change to that contract.
```

The first tells a reader nothing they could not guess from the title. The
second tells them whether they need to go there right now.

No checker can distinguish those, and `kb graph` does not try. It enforces
only **absence**: an article with no outbound edge at all fails the dead-end
ratchet. A reviewer catches emptiness.

---

## 5. The three generated artifacts

Two are **derived**. One records a **judgement**. Confusing them is the single
easiest way to end up with a gate that passes forever and means nothing.

### `.reverse-index.json` - derived

`source path → {articles, tests}`. Answers "I touched this file: what do I
read, and where do the test updates go?" as a lookup instead of a grep.

Derived from the KB, so stale means wrong. Regenerate it freely - a pre-commit
hook should.

`tests` comes from **real imports** plus exact-stem reconstruction, never
filename similarity. "The file with a similar name" is a guess that is wrong
exactly when the code has been refactored, which is when it matters.

Where the language's own tooling can answer the question, prefer it: a
`link_commands` entry runs `go list`, `madge` or equivalent and consumes
`<test>\t<source>` lines. That is the real dependency graph rather than an
approximation of it, including the cases a regex cannot see (build tags,
aliased imports, re-exports, generated code).

### The two ratchet baselines - derived

`.orphans-baseline.txt` (production source with no KB home) and
`.dead-ends-baseline.txt` (articles with no outbound edge).

A **ratchet** grandfathers what is already wrong, fails anything new, and
requires an entry that stops being wrong to be removed by regenerating. It may
shrink and never grow - `--write` refuses to widen either list.

That refusal is the whole mechanism. If regenerating could widen a baseline,
then adding an undocumented file and re-running `--write` would silently
legitimise it: the gate keeps passing and stops gating. That is worse than
failing, because nobody looks at a green build.

### `DIGEST.md` - derived

Every article's identity, edges, size and headline claims, generated from the
articles. The routing table an agent reads before choosing what to open: on a
270k-token corpus it is ~7k, so a wrong guess here is cheap and a wrong guess
there is not.

Because it is derived it can never disagree with the articles. A
hand-maintained summary would drift, and a drifted routing table sends agents
confidently to the wrong place.

### `.compiled-sources.json` - a judgement

A content hash per KB-owned source file, recorded at the last compile. It
asserts *these articles were checked against this content*.

**Nothing may regenerate it automatically.** Not a hook, not CI, not a
`--write` bundled into another target. The moment it regenerates on its own,
the check passes always and asserts nothing.

It is a flat, sorted, one-entry-per-line map with no header object, and the
shape is the design: two branches compiling different files touch disjoint
lines and merge cleanly; two compiling the same file conflict, correctly,
because both reviewed it and one is about to lose.

**What it cannot do:** tell whether the compile was any good. `kb compiled`
with no reading is indistinguishable from a careful pass. The gate buys you
"somebody was made to look at the right files", not "the article is correct".
Everything past that point is honesty.

---

## 6. Article types

**Optional.** Omit `article_types` and the taxonomy is not enforced at all:
articles may be named anything and `type:` is not required. The graph does not
need it, so a repo with existing documentation should not have to rename every
file to adopt this.

When you do configure it, types are enforced against the filename prefix. A
reasonable starting set:

| Prefix | `type` | Holds |
|---|---|---|
| `arch-` | `arch` | A subsystem: how it works, what must stay true |
| `recipe-` | `recipe` | An ordered procedure for a recurring task |
| `ref-` | `ref` | Lookup tables: signatures, fields, flags |
| `policy-` | `policy` | An obligation, often external (legal, contractual) |

The split matters because the sections differ. An `arch-` article is mostly
`INVARIANT` and `DECISION`; a `recipe-` article is mostly `STEPS` and ends
with `VERIFY`; a `ref-` article is a table and needs an owner who regenerates
it. A `policy-` article is what other articles are `gated_by:`.

---

## 7. Sizing

One article per **subsystem**, where a subsystem is a set of files that change
together and share invariants. Not one per directory - directory structure is
a filing decision and subsystem boundaries frequently cut across it.

Signals an article should split: two `INVARIANT` sets with no relationship to
each other; a reader who needs a third of it and must skim the rest.

Signals two should merge: an invariant restated in both (it will drift); an
edge between them that says "and also read the other half of this".

---

## 8. The rules a checker enforces

For reference, so you know what you get for free and what you must review for.

**Enforced, fails the build:**

- frontmatter present; `id` matches filename
- `type` matches the filename prefix, when a taxonomy is configured
- unknown frontmatter keys rejected
- `gated_by:` / `see_also:` resolve, and no self-edge
- intra-KB markdown links resolve
- every path in `## FILES` is tracked by git
- every backticked path in prose is tracked, or allowlisted with a reason
- a path that resolves only under some root is reported as *ambiguous*, not
  missing - the fix is to qualify it, not to hunt for the file
- every `covers:` glob matches at least one tracked file
- every article is linked from `INDEX.md`
- no new orphan; no new dead end; neither baseline grown
- the reverse index on disk matches a fresh regeneration
- no KB-owned file has changed since the last recorded compile

**Advisory, warns only:**

- a `§ Section` citation that names no heading in the target

**Not enforced, and cannot be:**

- whether an edge names an obligation or just a topic
- whether an `INVARIANT` is true
- whether a `DECISION` records the real reason
- whether the compile was actually performed

The last one is why the skills exist.
