---
id: arch-core
type: arch
covers:
  - app/core/**
see_also:
  - arch-api
---
# Scoring engine

The engine turns a raw value into a score. It is the only place clamping
happens, so a caller that clamps again will double-apply the bound.

## FILES

```
app/core/engine.py
app/core/util.py
```

## INVARIANT

`run()` clamps exactly once, in `util.clamp`. Adding a second clamp anywhere
upstream changes every score at the boundary without failing a test that
asserts mid-range values.

## VERIFY

`tests/test_core_engine.py` reaches the engine through the package, so no
import names `engine.py` directly — the flattened stem is what recovers the
link.

## SEE ALSO

- [arch-api.md](arch-api.md) — owns the wire shape `run()`'s output lands in,
  so a change to the return type is a change to that contract.
