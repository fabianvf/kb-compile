---
id: arch-api
type: arch
covers:
  - app/api/**
gated_by:
  - arch-core
---
# Request handling

The API layer is a thin dispatcher. It does no arithmetic of its own — every
number it returns came from the engine.

## FILES

| Path | Role |
|---|---|
| `app/api/handler.py` | Entry point; dispatches to the engine |
| `app/api/models.py` | Wire shapes — see `handler.py` for the caller |

## GOTCHA

`app/api/schema.json` is generated and gitignored, so it exists on a dev
machine and not in a fresh checkout. Avatar uploads land at
`uploads/{userId}/avatar.png`, which is an object-store key rather than a repo
path.

## SEE ALSO

- [recipe-add-endpoint.md](recipe-add-endpoint.md) — the checklist a new
  endpoint owes, including the suite-map entry without which nothing runs it.
