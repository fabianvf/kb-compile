---
id: ref-surface
type: ref
covers:
  - internal/config/config.go
  - cmd/kb/main.go
see_also:
  - arch-gates
  - arch-links
---
# Config schema and CLI surface

Everything repo-specific is data. This article is the reference for what a
`.kb/config.yaml` may contain and what the commands do; the reasoning behind
each check lives in the article that owns it.

## FILES

| Path | Role |
|---|---|
| `internal/config/config.go` | The schema, defaults and validation |
| `cmd/kb/main.go` | Command dispatch and the failure output |

## COMMANDS

```
kb graph [--write]     validate; --write regenerates the derived artifacts
kb fresh               fail if KB-owned source moved since the last compile
kb compiled            record a compile. Never from a hook
kb read ID[#SECTION]   print an article, or one section
kb eval                score the article boundaries against git history
```

## INVARIANT

**Failure output is written as instructions, not diagnostics.** Not every agent
reading these messages will have the KB's conventions loaded, and some cannot.
The error has to carry the fix. This is also what makes the tool
editor-agnostic: an agent that never loads a skill is still stopped by the
binary and told what to do.

**Every allowlist entry requires a written reason.** `not_repo_paths` and
`generated_paths` reject empty values. An entry without a reason is
indistinguishable from a mistake, and this is the argument that decided YAML
over JSON: the reason needs somewhere to live.

## DECISION

**YAML by default, JSON still accepted, both is an error.** The format is an
implementation detail of one schema - YAML is converted to JSON before decoding
so a single set of struct tags governs both and unknown-key rejection is
identical. Finding two configs is an error rather than a precedence rule,
because two means one is stale and silently preferring either lets an edit land
in the file nobody reads.

**Unknown keys are rejected.** A typo'd `covers:` silently owns nothing, which
is exactly the class of failure this tool exists to catch.

**An explicit `exclude_substrings` REPLACES the defaults.** Vendored trees are
excluded by default so adoption does not begin with thousands of orphans from
code nobody wrote, but a repo that genuinely documents its vendor tree must be
able to say so without fighting a merge.

## GOTCHA

**`SourceExtensions` defaults to the union of adapter extensions.** A test file
outside that set is skipped by the link builder entirely, which is correct for
a PNG fixture parked under a test directory and surprising if you configure
`link_commands` with no adapters and expect stem matching to still run.

## SEE ALSO

- [arch-gates.md](arch-gates.md) - reads `test_rules`, `prod_roots` and both
  allowlists; changing what counts as a test changes the ratchet's scope.
- [arch-links.md](arch-links.md) - owns the adapter and `link_commands`
  semantics these fields configure.
