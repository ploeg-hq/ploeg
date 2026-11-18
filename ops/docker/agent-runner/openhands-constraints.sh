#!/usr/bin/env bash
set -euo pipefail

version=${1:?usage: openhands-constraints.sh <openhands version>}

here=$(cd "$(dirname "$0")" && pwd)
overrides="$here/openhands-overrides.txt"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

git -c advice.detachedHead=false clone -q --depth 1 --branch "$version" https://github.com/OpenHands/OpenHands-CLI.git "$work/cli"
out="$here/openhands-$version.constraints.txt"
(cd "$work/cli" && uv export --frozen --no-hashes --no-dev --no-emit-project --no-header --no-annotate) >"$work/lock.txt"

awk 'NR == FNR { split($1, o, "=="); overridden[tolower(o[1])] = 1; next }
     { split($1, a, /[=<>; ]/); if (!(tolower(a[1]) in overridden)) print }' "$overrides" "$work/lock.txt" >"$out"

for old in "$here"/openhands-*.constraints.txt; do
  [ "$old" = "$out" ] || git -C "$here" rm -q "$old" 2>/dev/null || rm -f "$old"
done
printf 'wrote %s (%s pins; %s packages taken from %s instead)\n' "$out" "$(grep -c '==' "$out")" "$(grep -c '==' "$overrides")" "$(basename "$overrides")"
