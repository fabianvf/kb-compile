---
id: recipe-add-endpoint
type: recipe
---
# Adding an endpoint

1. Add the handler function to `app/api/handler.py`.
2. Add its wire shape to `app/api/models.py`.
3. Add a `map` line to `build/suite_map.txt` so a suite covers it.
4. Add a test under `tests/`.

This article is a deliberate DEAD END in the fixture: it has no outbound edge,
so it is grandfathered into `.dead-ends-baseline.txt` and a NEW dead end fails.
