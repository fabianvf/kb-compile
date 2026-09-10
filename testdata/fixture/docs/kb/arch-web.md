---
id: arch-web
type: arch
covers:
  - web/**
see_also:
  - arch-api
---
# Browser client

Renders a score. It formats in exactly one place so the unit suffix cannot
drift between surfaces.

## INVARIANT

`format()` is the only thing that appends a unit. A caller that concatenates
its own suffix produces "1 pts pts" in precisely the case nothing asserts.

## SEE ALSO

- [arch-api.md](arch-api.md) — supplies the number this renders, so a change
  to the wire shape lands here first.
