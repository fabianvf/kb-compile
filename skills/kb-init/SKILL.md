---
name: kb-init
description: Bootstrap an enforced LLM knowledge base in a repository that has none. Surveys the repo, writes .kb/config.yaml, creates the KB directory, wires the gates into the build and CI, installs the pre-commit hook, and records the first compile. Use when a repo has no docs/kb/ yet, or when adopting kb-compile for the first time.
argument-hint: [kb-directory]
allowed-tools: Bash, Read, Write, Edit, Glob, Grep
---

Bootstrap a knowledge base that the build enforces.

Read [`SPEC.md`](../../SPEC.md) first - it defines the article format, the
section vocabulary, and the derived-vs-judgement distinction that the rest of
this depends on.

**This skill sets up the machinery. It does not write the KB** - that is
`kb-catchup`, which this hands off to at the end.

---

## The one decision that decides whether this survives

On an existing repo, **the orphan baseline starts at whatever it is.**

You will be tempted to demand full coverage before turning the gate on. Don't.
A repo that adopts this at 700 orphans and gets a red build on day one deletes
the checker by day three. The ratchet is designed for exactly this: grandfather
everything, fail anything new, shrink over time.

Coverage is `kb-catchup`'s job and it is measured in weeks. Today's job is to
make the number stop going up.

**`kb-init` itself is cheap** (a survey, a config, one article: tens of
thousands of tokens). `kb-catchup` is not, and it scales with the orphan count.
When you report the baseline at the end, report the estimated catchup cost with
it, so the size of the commitment is visible before anyone opts into it.

---

## 1. Check the binary

```bash
kb version
```

If it is missing, install it and pin the version - a checker that silently
changes behaviour under a team is its own kind of drift. Prebuilt binaries
need no Go toolchain, which matters because the repo you are adopting this
into very often has none.

Get the tag first. Do not copy a version out of this file: it would be a
guess, and pinning to a release that does not exist fails at the install step
rather than at the gate, which reads as a broken repo rather than a broken pin.

```bash
gh release list --repo fabianvf/kb-compile --limit 1
```

Then, with `<tag>` as what that printed:

```bash
curl -fsSL -o /usr/local/bin/kb \
  https://github.com/fabianvf/kb-compile/releases/download/<tag>/kb_darwin_arm64
chmod +x /usr/local/bin/kb
kb version
```

Assets are `kb_{linux,darwin,windows}_{amd64,arm64}`, with `checksums.txt`
beside them. Use a tag in the URL rather than `latest/download` and write the
same tag into the CI step, so every machine runs the same checker. Check
`kb version` agrees with the tag you asked for before going further.

`go install github.com/fabianvf/kb-compile/cmd/kb@<tag>` is equivalent where Go
is already present, and a commit sha works in place of the tag if there is no
release to pin to yet.

## 2. Survey the repo

Gather what the config needs. Look, don't assume:

```bash
git ls-files | sed 's|/[^/]*$||' | sort | uniq -c | sort -rn | head -30
git ls-files | grep -oE '\.[a-z0-9]+$' | sort | uniq -c | sort -rn | head -20
```

You need answers to:

- **Is this repo even code?** Ask first, because the rest follows from it. In
  a GitOps or infrastructure repo the Kubernetes manifests, the Nix modules or
  the Terraform ARE the product, and `prod_extensions: [.yaml]` is as correct
  there as `.py` is elsewhere. Scoping such a repo to its handful of scripts
  leaves most of what runs undocumented while the gate reports green.
- **Which roots hold production source?** The ones the orphan ratchet will
  govern. Include everything that ships or runs: application code, functions,
  infra, rules files, the admin surface. Exclude vendored code and build output.
- **Which extensions?** What ships, not what supports it. The test is whether
  changing the file changes what runs. `.json` for a fixture, no; `.yaml` for
  a manifest Flux applies, yes.
- **How are tests named?** Both conventions usually coexist: a `_test`/`.spec`
  **suffix** and a `test_` **prefix**. Miss one and those files get treated as
  production source, so the ratchet demands KB ownership for a test.
- **Can the language's own tooling answer the import question?** Prefer a
  `link_commands` entry running `go list`, `madge`, `pydeps` or equivalent over
  a regex adapter. It is the real dependency graph rather than an
  approximation, and `kb imports LANG` has a resolver built in for Go, Python,
  TypeScript/JavaScript, Java, Dart and Rust - each needing only `python3` and
  `git`. Run `kb imports LANG --help` before wiring one in: it prints what
  that resolver does and which `test_rules` it assumes, and kb hard-fails when
  the two disagree. Omit it entirely where the language has no tests here. Fall back to an adapter
  where no such tool exists, and note that the `root` question is the one
  people get wrong there: is the import root the repo root (`""`) or a
  subdirectory?
- **What is the build entrypoint?** Makefile, `package.json` scripts,
  `justfile`, `Taskfile`, `cargo`, or bare CI.

**Ask rather than guess** when a root is ambiguous - whether generated clients,
migrations, or scripts count as production source is a real decision with real
consequences (every file in there will need an owning article eventually), and
it is cheap to ask now and expensive to reverse later.

## 3. Write the config

Write **`.kb/config.yaml`**. YAML rather than JSON because this file is read
and edited by people far more often than by programs, and comments are the
difference between a setting someone can change and one nobody dares touch.
The two allowlists in particular are worthless without their reasons written
beside them. JSON still loads if a repo already has one, but do not create new
ones, and never keep both: two configs means one is stale, and `kb` refuses to
guess which.

**Comment the choices as you make them.** Six months on, "why is `.yaml` not in
`prod_extensions`?" is a real question, and the config is the only place that
can answer it.

Start minimal and widen. This is a complete, valid config:

```yaml
prod_roots: [src]
prod_extensions: [.py]
```

Everything else is an opinion you can decline:

| Field | Add it when |
|---|---|
| `kb_dir` | the KB is not at `docs/kb` |
| `path_extensions` | articles cite files they do not own (configs, templates) |
| `article_types` | you want a taxonomy enforced. Omit and articles may be named anything |
| `test_rules` | **almost always. There is no default** - omit it and NOTHING is a test, so every test file under a prod root becomes an orphan, and a `link_commands` entry fails outright. Check BOTH conventions: a `_test`/`.spec` suffix and a `test_` prefix |
| `link_commands` | the language is supported AND the repo has tests in it. `command: [kb, imports, LANG]` for go, python, typescript, javascript, java, dart or rust; the resolver ships inside the binary. **Omit it where a supported language has no tests** - an empty result is a hard failure, so configuring one there fails the build. Prefer it to `adapters` |
| `adapters` | no such tool exists. The `root` question is the one people get wrong: is the import root the repo root (`""`) or a subdirectory? |
| `ambiguity_roots` | prose cites paths like `core/engine.py` that are not repo-root-relative |
| `exclude_dirs` | the defaults are wrong. Entries are directory NAMES matched against whole path segments (`vendor`, not `vendor/`). An explicit list REPLACES the defaults, so re-list what you still want |

Start `not_repo_paths` and `generated_paths` **empty**. They earn entries one
at a time, each with a written reason, when the checker flags something. If you
are adding a third in one sitting, the rule is probably wrong rather than the
paths.

`testdata/fixture/.kb/config.yaml` in the kb-compile repo is a fully commented
reference.

Then check it parses before going further:

```bash
kb graph 2>&1 | head
```

Expect it to fail on a missing KB directory. That is the correct failure at
this point; it means the config loaded.

**Re-run this step alone to change the config later** - adding a language,
widening a root, swapping an adapter for a `link_commands` entry. Regenerate
with `kb graph --write` afterwards and read the diff: widening `prod_roots` or
`prod_extensions` adds orphans, and the ratchet will refuse to write a grown
baseline, which is the check working.

## 4. Create the KB directory

**A fresh directory, even if the repo already has docs.** `docs/kb/` holds
articles, which are a different artifact from prose documentation: they carry
frontmatter, they claim ownership of source files, and every path in them is
checked. Pointing `kb_dir` at an existing `docs/` or `knowledge-base/` makes
every file in it an article, so every one fails frontmatter at once and the
gate cannot run until all of them are converted. The existing docs are not in
the way and do not need migrating - leave them alone.

Do not put the articles in `.kb/` beside the config either, however tidy that
looks. Ripgrep and `fd` skip hidden directories by default, so the KB would be
invisible to the search an agent runs when it does not yet know which article
it needs.

**Write TWO articles, not one.** An article with no outbound `## SEE ALSO`
edge is a dead end, so a single seed article is grandfathered into the
dead-ends baseline on the first write and the KB starts life owing a debt.
Two articles that genuinely point at each other start the graph at zero dead
ends and demonstrate the edge a third author will copy.

Pick two subsystems you know well and that really do relate - the thing and
the thing it calls - and write something true about each. The seeds set the
standard, and a placeholder is the standard everyone copies.

**Harvest the repo's existing prose before writing either one.** README files
at every level, a `docs/` or `knowledge-base/` tree, ADRs, incident writeups,
`AGENTS.md`:

```bash
git ls-files '*.md' '*.rst' '*.adoc' | xargs grep -lni '<subsystem name>'
```

Code comments are the easy source and they are right in front of you; prose
elsewhere in the repo is the valuable one and it is easy to miss. Writing a
seed from docstrings alone, with a doc in the tree that explains the same
subsystem better, produces an article that looks authoritative, gets indexed,
and is a thinner version of text that already existed. That is the exact
failure this happened to hit on its first real run - see kb-catchup step 3,
which carries the incident.

Where existing prose says it well, take it nearly verbatim. What you are
adding is frontmatter, file ownership, section structure and checked paths.

```
docs/kb/
├── INDEX.md          # links every article; an unlinked article fails
├── arch-<a>.md       # `## SEE ALSO` -> arch-<b>, with a reason
└── arch-<b>.md       # `## SEE ALSO` -> arch-<a>, with a reason
```

`INDEX.md` is the hub. Every article must be linked from it.

**`git add docs/kb` before running `kb graph`.** An article git does not track
is not part of the KB, and `kb graph` says so once anything under `docs/kb` is
committed. Staging is not committing; do it as you write.

If the repo already has `docs/kb` generated files committed but no articles -
a DIGEST and reverse index describing a KB that is not there - that is the
broken state step 7's hook exists to prevent, and you are adopting into it.
Delete the stale baselines and let step 5 recreate them:
`rm -f docs/kb/.orphans-baseline.txt docs/kb/.dead-ends-baseline.txt`. A
committed EMPTY baseline is the trap: `--init` is ignored when a baseline
exists and `--write` refuses to grow one, so without deleting it first
adoption cannot start at all.

A `## SEE ALSO` reason names a specific obligation or invariant in the target,
not a topic. "See arch-b for the cache" is a topic; "arch-b owns the eviction
order these writes depend on" is the reason the reader needs.

## 5. Generate the baselines

The first write is the one time a ratchet is allowed to start non-empty, so it
has to be asked for explicitly:

```bash
kb graph --write --init
```

Without `--init`, `--write` refuses to create a baseline with entries in it,
and it should: a missing baseline is indistinguishable from a deleted one, so
treating absence as "start fresh" would make `rm .orphans-baseline.txt` a way
to grandfather anything.

Use it once, here. It only has an effect when the baseline does not exist, so
it cannot widen anything later - after this step every regeneration is a plain
`kb graph --write`, which may only shrink.

This writes the reverse index and both baselines. **Read the orphan count out
loud to yourself** - from what `kb graph --write --init` printed, not from
`wc -l` on the baseline, which counts the header too - it is the size of the catchup job and worth knowing before
you promise anything.

Stage **the whole `docs/kb` directory** - the articles, `INDEX.md` and the
four generated files together. Hold the commit until step 9: the freshness
manifest is not written until then, so a commit made here fails `kb fresh` for
anybody who pulls it. They are part of the contract, not scratch
output, and committing the generated half without the articles is worse than
committing neither: the derived files then describe a KB the repo does not
have.

## 6. Wire it into the build

Two commands, in whatever the repo's lint entrypoint is:

```make
lint:
	kb graph
	kb fresh
```

Order matters: `kb graph` first. A broken graph makes the ownership map
untrustworthy, and a freshness verdict computed from a wrong ownership map is
worse than none - it reports a clean KB while articles point nowhere.

Add the same two to CI. If the repo has a `pre-push` or PR gate, `kb graph`
belongs there too.

**If the repo has no lint entrypoint, do not invent one.** Step 2 anticipates
"bare CI" and this is that case: put both commands in CI and rely on the hook
locally, rather than adding a Makefile to a repo that has never had one.

**A NEW CI file can itself fail the ratchet you just set.** If `.github/` is
inside a prod root, the workflow you add is production source with no owning
article. Add the two steps to an existing workflow, or claim the new one in a
seed article.

CI needs the binary, and installing it is one step - not a language toolchain.
Pin the same tag the team runs locally, substituting it for `<tag>` below, and
use a full clone: both gates ask git what the repo contains, and `kb eval`
walks history, so a shallow one makes them silently wrong rather than failing.

Run the workflow once and confirm the `kb` job actually ran. An install step
that 404s kills the job before either gate executes, and a workflow whose other
jobs pass reads as a repo that is merely a bit red rather than one where the KB
is not being checked at all.

```yaml
- uses: actions/checkout@v4
  with: { fetch-depth: 0 }
- name: install kb
  run: |
    curl -fsSL -o /usr/local/bin/kb \
      https://github.com/fabianvf/kb-compile/releases/download/<tag>/kb_linux_amd64
    chmod +x /usr/local/bin/kb
- run: kb graph
- run: kb fresh
```

## 7. Install the pre-commit hook

Regenerate the reverse index on commit, because it is **derived** - stale means
wrong, so rewriting it freely is correct:

```bash
#!/bin/sh
# Regenerate the DERIVED reverse index when the KB or tracked source moves.
# Deliberately does NOT run `kb compiled`: that records a human judgement, and
# a hook that regenerates a judgement makes the freshness gate pass always.
if git diff --cached --name-only | grep -qE '^(docs/kb/|<your-roots>/)'; then
  kb graph --write >/dev/null || exit 1
  # The whole KB directory, NOT just the four generated files.
  #
  # An earlier version of this hook staged only the generated ones. That is
  # the worst possible subset: the derived files name the articles they were
  # built from, so a commit carrying them without their source produces a repo
  # whose DIGEST and reverse index point at articles it does not contain, and
  # whose `kb graph` fails for everyone except the author. It happened in both
  # of the first two repositories to adopt this, unnoticed in both.
  git add docs/kb
fi
```

`kb graph` now refuses an untracked article once the KB has been committed, so
this hook is a convenience rather than the only thing standing between you and
that commit. Keep both.

Install the context hook too, on `UserPromptSubmit`. It injects the digest
once per session and attaches `kb owners` output for any file the message
names. Measured: two agents given a real bug in a repo whose `AGENTS.md` tells
them to look a file up before changing it both went straight to grep and never
opened the KB at all. Telling an agent the knowledge exists does not get it
read; putting it in front of them might.

```json
"UserPromptSubmit": [
  {"hooks": [{"type": "command", "command": "<plugin>/hooks/kb-context.sh"}]}
]
```

**Never add `kb compiled` to a hook.** If you take one thing from this skill,
take that. It is the line between a gate and a formality, and the failure is
invisible: everything stays green and nothing is ever checked.

## 8. Write the agent-facing instructions

Agents need to know the KB exists and is mandatory. Put the text in
**`AGENTS.md`**, which the widest range of tools read, and have any
tool-specific file import it rather than duplicating:

```markdown
## Knowledge base

Implementation knowledge, decision rationale and cross-file invariants live in
`docs/kb/` - see `docs/kb/INDEX.md`.

**Read the relevant article BEFORE reading source code or making changes.** It
holds context that is not in the code: which files must change together, which
plausible edit has already broken production, and why the current shape was
chosen.

To find what to read, look the file up rather than grepping:

    jq -r '.owners["path/to/file.py"]' docs/kb/.reverse-index.json

Before committing, run `kb fresh`. If it names articles, re-read every one of
them against the changed files, then run the exact command `kb fresh` told you
to run. Never record an article you did not open.
```

Duplicating this into a second file guarantees the copies diverge. One source,
imported.

**Point at what `kb fresh` prints, never at a command shape.** This text
outlives the binary the repo pins, and the two drift. Older builds end with
"record it with `kb compiled`" and accept no article names - on those, an
instruction to paste a scoped line and delete the articles you skipped reads as
"record only what I read" and records all of it, because the names are
discarded. Newer builds print the scoped command and honour it. Deferring to
the output is correct on both, because the line always comes from the same
binary that will consume it. A version number here would be a third thing to
keep in sync.

## 9. Record the first compile

```bash
kb compiled
```

Be honest about what this claims. It asserts every KB-owned file was reviewed
against its article. Right now that is true only because one article owns a
handful of files you just read. It stops being true the moment `kb-catchup`
starts claiming files, which is why catchup ends with its own `kb compiled`.

## 10. Verify, then hand off

```bash
kb graph && kb fresh && echo "gates green"
```

Then report, with the numbers rather than a summary:

- orphan baseline: N files (the catchup worklist)
- dead ends: N articles. Two seed articles pointing at each other makes this
  ZERO. Anything else means a seed has no outbound edge, and the baseline has
  just grandfathered it
- what got wired where

Then run **`kb-catchup`** to start shrinking the orphan baseline.

---

## Failure modes

**Turning the gate on before it can pass.** `kb graph` failing on day one for
reasons nobody has time to fix teaches everyone that the KB gate is noise. Get
to green with a large baseline first; shrink second.

**A config that owns too much.** Putting `.json` or `.yaml` in
`prod_extensions`, or a fixtures directory in `prod_roots`, produces hundreds
of orphans nobody will ever write an article for. Start narrow. Widening later
is one config line; a baseline full of files nobody intends to document is
permanent noise that trains people to ignore the list.

**A placeholder first article.** Whatever the first article looks like is what
every subsequent one will look like. Write a real one.
