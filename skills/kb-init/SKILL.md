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
changes behaviour under a team is its own kind of drift:

```bash
go install github.com/fabianvf/kb-compile/cmd/kb@latest   # or a release binary
```

## 2. Survey the repo

Gather what the config needs. Look, don't assume:

```bash
git ls-files | sed 's|/[^/]*$||' | sort | uniq -c | sort -rn | head -30
git ls-files | grep -oE '\.[a-z0-9]+$' | sort | uniq -c | sort -rn | head -20
```

You need answers to:

- **Which roots hold production source?** The ones the orphan ratchet will
  govern. Include everything that ships or runs: application code, functions,
  infra, rules files, the admin surface. Exclude vendored code and build output.
- **Which extensions?** Real source only. Adding `.json` here means every
  fixture needs an owning article.
- **How are tests named?** Both conventions usually coexist: a `_test`/`.spec`
  **suffix** and a `test_` **prefix**. Miss one and those files get treated as
  production source, so the ratchet demands KB ownership for a test.
- **Can the language's own tooling answer the import question?** Prefer a
  `link_commands` entry running `go list`, `madge`, `pydeps` or equivalent over
  a regex adapter. It is the real dependency graph rather than an
  approximation, and `contrib/` has a reference script. Fall back to an adapter
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
| `test_rules` | the defaults miss a convention. Check BOTH: a `_test`/`.spec` suffix and a `test_` prefix |
| `link_commands` | the language has real tooling (`go list`, `madge`). Prefer this to `adapters` |
| `adapters` | no such tool exists. The `root` question is the one people get wrong: is the import root the repo root (`""`) or a subdirectory? |
| `ambiguity_roots` | prose cites paths like `core/engine.py` that are not repo-root-relative |
| `exclude_substrings` | the defaults are wrong. An explicit list REPLACES them, so re-list what you still want |

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

After this, every regeneration is a plain `kb graph --write`, which may only
shrink the baselines.

**Re-run this step alone to change the config later** - adding a language,
widening a root, swapping an adapter for a `link_commands` entry. Regenerate
with `kb graph --write` afterwards and read the diff: widening `prod_roots` or
`prod_extensions` adds orphans, and the ratchet will refuse to write a grown
baseline, which is the check working.

## 4. Create the KB directory

`kb graph` needs at least one article, so create `INDEX.md` plus one real
article. Make it a genuine one - pick the subsystem you know best and write
something true, so the first article sets the standard rather than being a
placeholder everyone copies.

```
docs/kb/
├── INDEX.md          # links every article; an unlinked article fails
└── arch-<slug>.md
```

`INDEX.md` is the hub. Every article must be linked from it.

## 5. Generate the baselines

The first write is the one time a ratchet is allowed to start non-empty, so it
has to be asked for explicitly:

```bash
KB_RATCHET_INIT=1 kb graph --write
```

Without that variable `--write` refuses to create a baseline with entries in
it, and it should: a missing baseline is indistinguishable from a deleted one,
so treating absence as "start fresh" would make `rm .orphans-baseline.txt`
a way to grandfather anything. Use it once, here, and never again.

This writes the reverse index and both baselines. **Read the orphan count out
loud to yourself** - it is the size of the catchup job and worth knowing before
you promise anything.

Then commit these. They are part of the contract, not scratch output.

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
  git add docs/kb/.reverse-index.json docs/kb/.orphans-baseline.txt \
          docs/kb/.dead-ends-baseline.txt
fi
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

Before committing, run `kb fresh`. If it names articles, re-read them against
the changed files and record the review with `kb compiled`.
```

Duplicating this into a second file guarantees the copies diverge. One source,
imported.

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
- dead ends: N articles
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
