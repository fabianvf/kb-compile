#!/bin/sh
# Tier 1 end to end. Deterministic, no model, safe to run on every change.
set -eu
N="${1:-24}"
python3 bench/fetch.py --limit "$N" --per-repo 3
python3 bench/prepare.py --mode archive
python3 bench/route.py --json bench/results.json
