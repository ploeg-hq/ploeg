#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
usage() { echo "usage: $0 <image> | --dir <directory>" >&2; exit 2; }
fail() { echo "go toolchain: $*" >&2; exit 1; }

required=$(awk '$1 == "toolchain" { sub(/^go/, "", $2); print $2; exit }' "$root/go.mod")
[ -n "$required" ] || required=$(awk '$1 == "go" { print $2; exit }' "$root/go.mod")
[[ "$required" =~ ^[0-9]+\.[0-9]+(\.[0-9]+)?$ ]] || fail "go.mod names no Go release: '$required'"

case "${1:-}" in
  --dir) [ -n "${2:-}" ] && [ -d "$2" ] || usage; tree=$2; subject=$2 ;;
  "" | -*) usage ;;
  *)
    subject=$1
    tree=$(mktemp -d)
    container=""
    cleanup() { [ -z "$container" ] || docker rm -f "$container" > /dev/null 2>&1 || true; rm -rf "$tree"; }
    trap cleanup EXIT
    container=$(docker create "$subject" /nonexistent)
    docker export "$container" | tar -x -C "$tree" --no-same-owner --exclude='dev/*' 2>/dev/null || true
    ;;
esac

older() {
  [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -1)" = "$1" ]
}

found=0
stale=()
while IFS= read -r line; do
  binary=${line%%: go*}
  version=${line##*: go}
  version=${version%% *}
  [[ "$version" =~ ^[0-9]+\.[0-9]+(\.[0-9]+)?$ ]] || { stale+=("${binary#"$tree"} built with unrecognised go$version"); continue; }
  found=$((found + 1))
  if older "$version" "$required"; then
    stale+=("${binary#"$tree"} built with go$version")
  fi
done < <(go version "$tree" 2>/dev/null | grep -E ': go')

[ "$found" -gt 0 ] || [ "${#stale[@]}" -gt 0 ] || fail "$subject carries no Go binary to check"
if [ "${#stale[@]}" -gt 0 ]; then
  printf '%s\n' "${stale[@]}" >&2
  fail "$subject ships a Go binary older than go.mod's toolchain go$required; build with GOTOOLCHAIN=auto or a builder at go$required"
fi
echo "go toolchain: $found Go binaries in $subject are built with go$required or newer"
