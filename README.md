# kb-compile

Keeps a repository's LLM knowledge base honest, mechanically.

A KB that agents are told to read first is a contract. When it drifts, the
failure is silent and it is the *worst* kind of silent: the agent doesn't skip
the stale article, it acts on it. This enforces the two things that stop that.

```
kb graph      Do the KB's edges RESOLVE, and is every source file owned?
kb fresh      Has anyone LOOKED at the KB since the code moved?
kb compiled   Record that a compile reviewed the current content. Name the
              articles you read, or record everything with no argument.
kb read       Print one article, or one SECTION of one article.
kb owners     I'm about to change this file: what do I need to know?
kb eval       Score the article boundaries against git history.
kb imports    Resolve a language's real import graph, for `link_commands`.
kb task       An agent's uncommitted working notes for one task.
kb version    Which build is this? A build that can't say on its own carries
              a hash of its own bytes.
```

Single static binary, no runtime. The repo describes itself in
`.kb/config.yaml`; the checker supplies the rules.

## Why two gates

They fail differently, and only one of them produces a confidently-wrong
article.

**`kb graph`** validates structure. Every article declares `id`, what it
`covers:`, and optionally a `type`, what it is `gated_by:`, and where it points
next. The checker fails on a
dangling article link, a `covers:` glob that matches nothing, a path an article
names that git doesn't track, and a path that isn't repo-root-relative
(`core/engine.py` is ambiguous, and ambiguity is how a stale path hides).

It also runs two **ratchets**:

- **orphans** - production source with no KB home. A *new* orphan fails; an
  entry that stops being an orphan must be removed by regenerating. The list
  can only shrink.
- **dead ends** - articles with no outbound `## SEE ALSO` edge. An article
  nothing points out of can only be left via `INDEX.md`, which is the star
  topology a typed graph exists to replace.

And it generates `.reverse-index.json`: source path → `{articles, tests}`.
That answers "I touched this file: what do I read, and where do the test
updates go?" as a lookup instead of a grep.

**`kb fresh`** validates *attention*. `.compiled-sources.json` holds a content
hash per KB-owned file, recorded at the last compile. The check fails when any
of them differs from what was reviewed, and names the owning articles so the
re-read is scoped rather than "go read the KB". It ends by printing the
`kb compiled` line for exactly those articles, so recording what you read is a
paste and recording less is deleting names from it.

All three commands warn about production source git doesn't track yet, because
ownership comes from `git ls-files`. A subsystem you've written and not staged
has a `covers:` glob matching nothing and a compile marker narrower than you
think it is, and that narrowing is silent and looks exactly like success.
`kb graph` names every such file; `kb compiled` names them again as it writes
the judgement, scoped to the articles you asked for so another session's work
in progress isn't reported as yours - except for paths no article claims at
all, which are nobody's work in progress and are always named. `kb fresh`
reports exactly that slice. It is the one case the orphan ratchet can't reach,
since it walks the tracked list too, and an article and the code it covers
usually land in the same commit.

## Reading the KB without loading it

The consumer is an agent with a context budget, and the arithmetic is brutal.
Measured on a real 35-article KB: **~270k tokens for the corpus**, ~6k for the
median article, ~24k for the largest. A hub file of links tells an agent every
article's name and nothing about what any of them knows, so the cheapest way
to answer "where does scoring happen" was to guess and open a 24k article.

Two generated artifacts close that gap.

**`DIGEST.md`** is every article's identity, edges, size and headline claims,
at ~150 tokens each. On that same KB it is **7.4k against 270k**: the whole
corpus becomes legible for under 3% of it, and the agent opens one article on
purpose instead of two by trial. Derived, so it regenerates freely and cannot
drift from the articles it routes to. This is the repo analogue of `llms.txt`.

**`kb read <article>#SECTION`** makes the `§ SECTION` citation convention
executable. On the 24k article above, pulling just its `INVARIANT` section
costs **3.3k**. A wrong section name lists the real ones rather than failing
bare, so a bad guess does not cost a second round trip.

`max_article_tokens` warns when an article passes the size where it stops
being read in full (default 12k, advisory). An agent under context pressure
reads the first third; what is past that is functionally not in the KB while
still costing a review on every compile.

## Does it actually help?

`kb eval` answers the one part of that question that needs no model in the
loop: **do the files that change together share an article?**

For each historical commit touching two or more KB-owned files, it takes the
articles owning any one of them and asks how much of the rest of the change
those articles also cover. Recall alone is gamed by a single article owning the
repo, so it reports two bounds: the **reach** (how many files you had to read
to get that recall) and a **directory baseline** (the same measurement with
each folder treated as an article, which is what the tree already tells you for
free).

Without the control, a recall number is not a result. On the KB above:

```
KB          recall 0.57   reading 81 files on average
directories recall 0.41   reading 26 files on average
```

Sixteen points of boundary information the tree does not carry, bought with 3x
the reading. It also prints the worst-scoring changes, which are exactly the
file sets that move together and that no single article describes: the signal
for a merge.

What it does not measure is whether an article's prose is any good. A KB of
perfectly-bounded articles full of restated code scores well here.

## The distinction the whole design rests on

Some generated files are **derived**. `.reverse-index.json` is computed from
the KB, so stale means wrong, and a hook may rewrite it freely.

`.compiled-sources.json` is not derived. It records a **judgement** - *these
articles were checked against this content*. Regenerate a judgement
automatically and the check passes always and means nothing.

So `kb compiled` is never run by a hook. That is the difference between a gate
and a formality, and it is the single most important line in this README.

Same shape, one level up: a **ratchet** may shrink and never grow. If `--write`
can widen a baseline, then regenerating after adding an undocumented file
silently legitimises it - the gate keeps passing and stops gating, which is
worse than failing, because nobody looks.

What none of this can do is tell whether the compile was any *good*. A
`--write` with no reading is indistinguishable from a careful pass. The gate
buys you "somebody was made to look at the right files", not "the article is
correct".

## Task memory

A long-running agent needs to drop its raw trajectory without losing where it
is. `kb task` keeps notes in `.kb/tasks/<id>/`, which git ignores:

```
kb task start fix-eviction --goal "stop evicting pinned entries"
kb task commit "dropped the LRU approach" <<< "pins would need a second index"
kb task context     # the resume view, under ~1.5k tokens
kb task finish      # then the kb-harvest skill
```

A task log **never** writes an article and never runs `kb compiled`. It is
unverified scratch, and a green `kb fresh` means somebody reviewed the articles.
Let a log become KB content without that and the gate stops meaning anything. So
`kb-harvest` only proposes itemized deltas, `kb-compile` accepts or rejects each
one as evidence rather than authority, and then the notes are deleted.

The log's value to the KB is the why. The diff already carries the what, and
the alternative somebody tried and rejected is written down nowhere else. Most
tasks produce nothing durable, and that is the expected result.

Harvests are reviewed even when `kb fresh` is green. The commit gate compiles work
as it lands, so by the time a task finishes its files are usually already clean.

## Per-file state, deliberately

An earlier design used a single `last_compiled_commit` stamp and diffed trees
against it. That made every **merge** look like drift: bringing trunk down into
a branch changes the tree, so the branch owed a compile for files already
reviewed on the other side. Three consecutive merge-downs produced three
stamp-only "reviewed, already documented" commits. The check was measuring
merges, not drift.

Per-file hashes fix that by construction. Two branches compiling different
files update disjoint entries and git merges them like any other file. Two
compiling the *same* file conflict - correctly, because both reviewed it and
one is about to lose.

The manifest is therefore a flat, sorted, one-entry-per-line map with no header
object. The shape *is* the design.

## Configuration

Everything repo-specific is data, in `.kb/config.yaml`. A minimal config is
two lines:

```yaml
prod_roots: [src]
prod_extensions: [.py]
```

`testdata/fixture/.kb/config.yaml` is a fully commented reference. YAML because
the file is read and edited by people, and the allowlists are worthless without
their reasons beside them; JSON still loads for repos that already have one,
but keeping both is an error rather than a precedence rule.

Most settings are opinions you can decline. The article-type taxonomy is
optional; omit it and articles may be named anything. Vendored trees
(`node_modules/`, `vendor/`, `site-packages/`) are excluded by default, so
adoption does not start with thousands of orphans from code you did not write.

Language support is **declarative** - adding one must not require a Go
toolchain:

```yaml
- name: python
  extensions: [.py]
  pattern: '(?m)^\s*(?:from|import)\s+([a-zA-Z_][\w.]*)'
  strategy: dotted-longest-prefix
  root: ""             # the import root IS the repo root
  underscore_root: app # but pytest flattens relative to app/
  underscore_split: true
```

Three strategies cover the dialects seen so far:

- **`template`** - capture groups fill a path (`package:myapp/x.dart` → `lib/x.dart`)
- **`dotted-longest-prefix`** - a dotted module path, longest prefix first,
  because a package may re-export a submodule. `root: ""` means the repo root.
- **`relative`** - a `./`-style specifier resolved against the importing file

### Delegating to real tooling

Regex adapters approximate a resolver. `link_commands` asks the real one:

```yaml
link_commands:
  - name: go
    command: [kb, imports, go]
```

The command emits `<test file>\t<production file>`, one edge per line, both
repo-root-relative. Nothing about the language reaches `kb`, so a repo teaches
it about its own toolchain by supplying a script rather than waiting for a
strategy to be added here. Adapters and commands merge, for repos whose
languages are not all served by one approach.

Measured against the regex adapters on a 505-file Go repo: `go list` found 8
tests for `engine/engine.go` where the regex found 4, and correctly found none
for `cmd/analyzer/main.go` where the regex produced a match from an unrelated
program.

`kb imports LANG` carries a resolver for six languages **inside the binary**,
so nothing of this tool has to be copied into your repository and the resolver
is versioned with the checker that reads its output. Each needs only `python3`
and `git` - no language toolchain, no installed dependency tree - so the answer
is a function of the committed files and cannot differ between a fresh clone
and a working machine:

| `kb imports` | Resolves |
|---|---|
| `go` | `go list`, unioning `IgnoredGoFiles` so the answer does not change with `GOOS` |
| `python` | `ast`, with `src/` layouts and `from pkg import mod` preferred over the package's `__init__.py` |
| `typescript` / `javascript` | relative and tsconfig `paths` specifiers, `/index` resolution, and `.js` specifiers that are `.ts` on disk |
| `java` | explicit imports, wildcard imports, and same-package references with no import at all |
| `dart` | `package:` through every pubspec in the tree, following `part` directives into the library |
| `rust` | `mod` declarations walked from each crate root, hyphenated crate names included |

Rust has a caveat worth reading before you configure it: its usual
`#[cfg(test)] mod tests` lives inside the file it tests, which is not a
cross-file edge, so a crate with only inline tests correctly produces nothing.
`kb imports rust` says so on stderr rather than leaving you to guess.

An unlisted language is not something to wait for. A link command is any
program that emits the two columns, so point `command:` at your own.

A failing command is a hard error, not an empty column, because an empty
column reads as "no links needed". So is a command that exits 0 having emitted
nothing.

Test links come from **real imports**, never filename similarity - "the file
with a similar name" is a guess that's wrong exactly when the code has been
refactored, which is when it matters. Exact-stem reconstruction is a second
source, for tests that reach their subject through a dispatcher and so never
import it by name.

### Extra link kinds

Some relationships leave no import edge - an end-to-end suite selector covers a
screen flow, not a file. `extra_link_kinds` adds a column to the reverse index,
read from a file the repo already generates:

```yaml
- name: suites
  source: build/suite_map.txt
  pattern: '(?m)^map\s+"([^"]+)"\s+"([^"]*)"'
  match: substring
```

`match` is `exact` (default), `prefix`, or `substring`, and getting it wrong is
uniquely nasty: reading pattern keys as exact paths yields a silently **empty**
column, and an empty list reads as "no links needed" rather than as a bug. It
fails worst for the broadest keys - the ones matching everything are the least
likely to be spelled as a full path - so a whole subsystem's links vanish while
the narrow ones look fine. Check a known-broad key against the generated index
before trusting the column.

## Errors are written as instructions

Not every agent reading these messages will have loaded the KB's conventions,
and some won't be able to. The failure output has to carry the fix:

```
kb graph: docs/kb/arch-core.md:14: FILES block names `app/core/renamed.py`,
  which git does not track. Paths must be repo-root-relative. If it is generated
  and gitignored, add it to `generated_paths` in .kb/config.yaml with a reason.
```

This is also what makes the tool editor-agnostic. Instructions are an
optimisation; the binary is the contract.

## Testing

`go test ./...` is hermetic. `testdata/fixture` is a complete miniature
repository - a KB with both `## FILES` shapes, both edge kinds, a deliberate
orphan and a deliberate dead end, three import dialects, and a selector file
whose keys are patterns rather than paths. Its committed artifacts are the
goldens, and the tests stage it into a temp dir, `git init` it, regenerate, and
require byte-identity.

Byte-identity is the bar rather than "no errors", because every one of these
artifacts is a gate input: a reverse index that is merely *plausible* sends an
agent to the wrong article.

The suite asserts both directions. Six breakage cases prove each check
actually fires - a gate nobody has ever seen fail is a gate nobody knows works.

The same harness runs against any real repository, read-only, which is how a
port or a config change is validated against a KB some other implementation
generated:

```bash
KB_COMPARE_REPO=/path/to/repo KB_COMPARE_CONFIG=/abs/path/config.yaml \
  go test ./internal/kb -run Golden -v
```

### On mutation testing

The rules in `judgement_test.go` are there because a mutation pass over the
golden harness left them alive. The corpus contained no case that
distinguished, for instance, dropping an ambiguous path reconstruction from
accepting the first candidate.

A mutation no case catches is a hole in the corpus, not a clean bill of health.
Each survivor got a constructed case rather than a shrug.

## Skills

The binary enforces; the skills do the work it cannot. Installed together as a
Claude Code plugin, they read [`SPEC.md`](SPEC.md) as their shared reference.

| Skill | When |
|---|---|
| `kb-init` | A repo with no KB. Surveys it, writes the config, wires the gates into the build and CI, records the first compile. |
| `kb-catchup` | A large orphan baseline. Clusters undocumented files into subsystems by **co-change**, writes one article each, ratchets down in verified batches. |
| `kb-compile` | Before committing. Uses `kb fresh` output as the exact work list, reconciles each named article, reviews finished task notes, records the review. |
| `kb-harvest` | When a task finishes. Turns its notes into proposed deltas for `kb-compile`, or says nothing durable came out of it. |
| `kb-compress` | Quarterly. Deduplicates, collapses decision logs into rules, resizes articles, strengthens edges. |

`hooks/` ships a `PreToolUse` gate that blocks `git commit` while `kb fresh` is
red and hands the agent the list of articles to re-read. It never runs `kb
compiled` - a hook that records the judgement makes the check pass always.

The instructions are an optimisation, not the contract. An agent that never
loads a skill still gets stopped by the binary, which is why every failure
message is written as a fix rather than a diagnostic.

### Installing

Two halves, and the plugin is only one of them. The skills and the commit hook
come from the plugin; the gates they invoke are the `kb` binary, and the plugin
cannot install it for you.

```bash
/plugin marketplace add fabianvf/kb-compile
/plugin install kb-compile
```

Then the binary. Prebuilt, no Go toolchain required - pick your platform from
`kb_{linux,darwin,windows}_{amd64,arm64}`:

```bash
curl -fsSL -o /usr/local/bin/kb \
  https://github.com/fabianvf/kb-compile/releases/latest/download/kb_darwin_arm64
chmod +x /usr/local/bin/kb
kb version
```

`checksums.txt` ships alongside them. Pin a release rather than tracking
`latest` where more than one person runs the gate: a checker that changes
behaviour under a team is its own kind of drift. Replace `latest/download`
with `download/<tag>`, taking the tag from the releases page.

With Go on the machine, `go install github.com/fabianvf/kb-compile/cmd/kb@latest`
works too, and reports the module version it was built from. A commit works in
place of `latest` and needs no release artifact, which is the easier pin if you
are already installing from source.

A build whose version can't identify it on its own - a `+dirty` tree, or `dev`
- appends a hash of the binary's own bytes. Go derives `+dirty` from whether a
tree has uncommitted changes and not from what is in them, so without that
every build from one working tree reports the same string and "is this binary
current?" has no answer short of running it. Tagged builds get no suffix, since
they are already the thing you pin.

In CI, the same curl line is usually all that is needed - no `setup-go` step,
because the gate is a static binary and not a Go project concern.

For other editors, `kb-init` writes the KB section into **`AGENTS.md`** and has
any tool-specific file import it, so there is one copy rather than several that
drift.

## This repo uses it

`docs/kb/` is kb-compile's own knowledge base, and CI runs `kb graph` and
`kb fresh` against it on every push. A change that breaks the contract fails
the build that ships the thing enforcing it, which is the only arrangement
where the contract stays honest.

It works. Writing these four articles, the gate caught a change to `eval.go`
made after the compile was recorded and named the owning article to re-read.
That is the intended failure, on its author, within an hour of the KB existing.

## Provenance

Ported from a Dart implementation that ran in one repository for months. The
value was never the code - it was a dozen judgement calls buried in it:

- which prose tokens count as a path, and which are just prose
- that a `## FILES` **table** is an ownership claim just as much as a fenced
  block, but a bare filename in a table cell is not
- that `git ls-files` and not the filesystem decides what exists - probing the
  filesystem is what made the original pass locally and fail in CI
- that cross-language stem matching needs a distinctive stem, or it links every
  `app.js` to every `app_test.dart`
- that an ambiguous reconstruction must yield *nothing*, because a confident
  wrong answer sends an agent to a test that guards something else

A rewrite that drops any one of those produces a checker that passes and gates
nothing. Every one is pinned by a test here.
