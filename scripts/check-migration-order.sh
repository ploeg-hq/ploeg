#!/usr/bin/env bash
set -euo pipefail
if [ $# -lt 1 ] || [ $# -gt 2 ]; then
  echo "usage: check-migration-order.sh <base> [head]" >&2
  exit 2
fi
base=$1
head=${2:-}
directory=pkg/store/migrations
legacy_duplicate_ceiling=36

migrations_at() {
  if [ -z "$1" ]; then
    find "$directory" -maxdepth 1 -type f -name '[0-9]*_*.sql' -exec basename {} \; 2>/dev/null | sort
  else
    git ls-tree --name-only "$1" "$directory/" | sed 's|.*/||' | grep -E '^[0-9]+_.*\.sql$' | sort || true
  fi
}

number_of() {
  local digits=${1%%_*}
  echo $((10#$digits))
}

git rev-parse --verify --quiet "$base^{commit}" >/dev/null || { echo "check-migration-order: base $base is not a commit in this clone" >&2; exit 2; }
base_files=$(migrations_at "$base")
head_files=$(migrations_at "$head")

highest=0
while IFS= read -r file; do
  [ -n "$file" ] || continue
  number=$(number_of "$file")
  if [ "$number" -gt "$highest" ]; then
    highest=$number
  fi
done <<<"$base_files"

failed=0
while IFS= read -r file; do
  [ -n "$file" ] || continue
  if ! grep -qxF "$file" <<<"$base_files"; then
    number=$(number_of "$file")
    if [ "$number" -le "$highest" ]; then
      echo "$directory/$file: added migration number $number is at or below $highest, the highest on the base branch. Renumber it above $highest." >&2
      failed=1
    fi
  fi
done <<<"$head_files"

duplicates=$(while IFS= read -r file; do
  if [ -n "$file" ]; then
    number_of "$file"
  fi
done <<<"$head_files" | sort -n | uniq -d)
for number in $duplicates; do
  if [ "$number" -gt "$legacy_duplicate_ceiling" ]; then
    echo "$directory: more than one migration is numbered $number: $(grep -E "^0*${number}_" <<<"$head_files" | tr '\n' ' ')" >&2
    failed=1
  fi
done

if [ "$failed" -ne 0 ]; then
  exit 1
fi
echo "Migrations are in order above $highest, the base branch's highest."
