#!/usr/bin/env bash
set -euo pipefail

version=${1:?usage: openhands-constraints.sh <openhands version>}
musl_wheel_pins=(psutil==7.2.2)

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

git -c advice.detachedHead=false clone -q --depth 1 --branch "$version" https://github.com/OpenHands/OpenHands-CLI.git "$work/cli"
out="$here/openhands-$version.constraints.txt"
(cd "$work/cli" && uv export --frozen --no-hashes --no-dev --no-emit-project --no-header --no-annotate) >"$out"

for pin in "${musl_wheel_pins[@]}"; do
  name=${pin%%==*}
  grep -q "^$name==" "$out" || continue
  sed -i "s/^$name==[^ ;]*/$pin/" "$out"
done

for old in "$here"/openhands-*.constraints.txt; do
  [ "$old" = "$out" ] || git -C "$here" rm -q "$old" 2>/dev/null || rm -f "$old"
done
printf 'wrote %s (%s pins)\n' "$out" "$(grep -c '==' "$out")"
