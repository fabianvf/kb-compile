# kb-compile

Keeps a repository's LLM knowledge base honest, mechanically.

A KB that agents are told to read first is a contract. When it drifts, the
failure is silent and it is the *worst* kind of silent: the agent doesn't skip
the stale article, it acts on it. This enforces the two things that stop that.

```
kb graph      Do the KB's edges RESOLVE, and is every source file owned?
kb fresh      Has anyone LOOKED at the KB since the code moved?
kb compiled   Record that a compile just reviewed the current content.
```

Single static binary, no runtime. The repo describes itself in
`.kb/config.json`; the checker supplies the rules.

## Why two gates

They fail differently, and only one of them produces a confidently-wrong
article.

**`kb graph`** validates structure. Every article declares `id`, `type`, what
it `covers:`, and optionally what it is `gated_by:`. The checker fails on a
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
re-read is scoped rather than "go read the KB".

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

Everything repo-specific is data. `testdata/fixture/.kb/config.json` is a
complete worked example.

Language support is **declarative** - adding one must not require a Go
toolchain:

```jsonc
{
  "name": "python",
  "extensions": [".py"],
  "pattern": "(?m)^\\s*(?:from|import)\\s+([a-zA-Z_][\\w.]*)",
  "strategy": "dotted-longest-prefix",
  "root": "",
  "underscore_root": "app",
  "underscore_split": true
}
```

Three strategies cover the dialects seen so far:

- **`template`** - capture groups fill a path (`package:myapp/x.dart` → `lib/x.dart`)
- **`dotted-longest-prefix`** - a dotted module path, longest prefix first,
  because a package may re-export a submodule. `root: ""` means the repo root.
- **`relative`** - a `./`-style specifier resolved against the importing file

### Delegating to real tooling

Regex adapters approximate a resolver. `link_commands` asks the real one:

```jsonc
"link_commands": [
  { "name": "go", "command": ["contrib/kb-imports-go.sh"] }
]
```

The command emits `<test file>\t<production file>`, one edge per line, both
repo-root-relative. Nothing about the language reaches `kb`, so a repo teaches
it about its own toolchain by supplying a script rather than waiting for a
strategy to be added here. Adapters and commands merge, for repos whose
languages are not all served by one approach.

Measured against the regex adapters on a 505-file Go repo: `go list` found 8
tests for `engine/engine.go` where the regex found 4, and correctly found none
for `cmd/analyzer/main.go` where the regex produced a match from an unrelated
program. `contrib/kb-imports-go.sh` is the reference implementation.

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

```jsonc
{
  "name": "suites",
  "source": "build/suite_map.txt",
  "pattern": "(?m)^map\\s+\"([^\"]+)\"\\s+\"([^\"]*)\"",
  "match": "substring"
}
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
  and gitignored, add it to `generated_paths` in .kb/config.json with a reason.
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
KB_COMPARE_REPO=/path/to/repo KB_COMPARE_CONFIG=/abs/path/config.json \
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
| `kb-compile` | Before committing. Uses `kb fresh` output as the exact work list, reconciles each named article, records the review. |
| `kb-compress` | Quarterly. Deduplicates, collapses decision logs into rules, resizes articles, strengthens edges. |

`hooks/` ships a `PreToolUse` gate that blocks `git commit` while `kb fresh` is
red and hands the agent the list of articles to re-read. It never runs `kb
compiled` - a hook that records the judgement makes the check pass always.

The instructions are an optimisation, not the contract. An agent that never
loads a skill still gets stopped by the binary, which is why every failure
message is written as a fix rather than a diagnostic.

### Installing

```bash
/plugin marketplace add fabianvf/kb-compile
/plugin install kb-compile
```

For other editors, `kb-init` writes the KB section into **`AGENTS.md`** and has
any tool-specific file import it, so there is one copy rather than several that
drift.

## Status

Working: both checkers, the config layer, the golden harness, `SPEC.md`, four
skills, the commit hook.

Untried: everything past this repo. `kb-init` and `kb-catchup` have not been
run against a real codebase yet, and the config survey in `kb-init` is the part
most likely to need adjusting on first contact.

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
