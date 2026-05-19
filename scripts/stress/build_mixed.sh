#!/usr/bin/env bash
# Regenerate scripts/stress/queries/mixed.txt deterministically.
# Composition:
#   50 cache-hot names    hot-0001.test … hot-0050.test
#   9000 cold names       cold-0001.test … cold-9000.test
#   950 blocked names     subset of hagezi pro.txt
set -euo pipefail

cd "$(dirname "$0")/queries"

HAGEZI="${HAGEZI:-/home/ben.guest/vendor/hagezi-dns-blocklists/domains/pro.txt}"
if [[ ! -f "$HAGEZI" ]]; then
    echo "hagezi pro.txt not found at $HAGEZI" >&2
    exit 1
fi

# Frozen subset: deterministic by sorting then sampling every 495th line,
# capped at 950 entries. Re-running with a fresh hagezi snapshot updates the set.
grep -v '^#' "$HAGEZI" | sort | awk 'NR % 495 == 0' | head -n 950 > blocked-sample.txt
echo "wrote $(wc -l < blocked-sample.txt) blocked names to blocked-sample.txt"

{
    seq -f "hot-%04g.test" 1 50
    seq -f "cold-%04g.test" 1 9000
    cat blocked-sample.txt
} > mixed.txt
echo "wrote $(wc -l < mixed.txt) total names to mixed.txt"
