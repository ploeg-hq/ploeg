#!/usr/bin/env bash
# Confirmation for ADR-0022: the name and mark are trademarks under a usage
# policy, and the artwork files keep the repository's single Apache-2.0 answer.
#
# Fails if the policy goes missing, if a second copyright answer appears next to
# the assets, or if the policy stops being reachable from the front door.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
fail=0

note() { printf '  %s\n' "$1"; fail=1; }

echo "ADR-0022 — name and mark"

[ -f docs/brand/TRADEMARK.md ] \
  || note "missing docs/brand/TRADEMARK.md — the policy IS the grant; without it the mark has no stated terms"

stray="$(find docs/brand -maxdepth 1 -type f \( -iname 'LICENSE*' -o -iname 'COPYING*' \) 2>/dev/null || true)"
[ -z "$stray" ] \
  || note "second copyright answer under docs/brand/: ${stray//$'\n'/, } — ADR-0022 keeps the assets under the root Apache-2.0"

grep -q 'docs/brand/TRADEMARK.md' README.md \
  || note "README.md no longer links docs/brand/TRADEMARK.md — an unreachable policy is not a policy"

if [ "$fail" -ne 0 ]; then
  echo "FAIL — see docs/adrs/0022-the-name-and-mark-are-trademarks-not-cc-licensed-artwork.md"
  exit 1
fi
echo "ok"
