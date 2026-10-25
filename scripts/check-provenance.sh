#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
record="${PROVENANCE_RECORD:-PROVENANCE.md}"
check_source=0
case "${1:-}" in
  "") ;;
  --source) check_source=1 ;;
  *) echo "usage: $0 [--source]" >&2; exit 2 ;;
esac

failures=0
fail() { echo "provenance: $1" >&2; failures=$((failures + 1)); }
value() { awk '/^```provenance$/ { inside = 1; next } /^```$/ { inside = 0 } inside' "$record" | sed -n "s/^$1: *//p"; }
required() {
  local v
  v=$(value "$1")
  [ -n "$v" ] || { echo "provenance: $record has no '$1' in its \`\`\`provenance block" >&2; exit 1; }
  printf '%s' "$v"
}
utc_date() { TZ=UTC git -C "$1" log -1 --date=format-local:%Y-%m-%d --format=%cd "$2"; }

root_commit=$(required root-commit)
root_tree=$(required root-tree)
root_date=$(required root-date)
source_commit=$(required source-commit)
source_path=$(required source-path)

if ! git rev-parse --git-dir >/dev/null 2>&1; then
  echo "provenance: $root is not a Git clone, so its history cannot be checked; clone the repository instead of downloading an archive" >&2
  exit 1
fi
if [ "$(git rev-parse --is-shallow-repository)" = true ]; then
  echo "provenance: this clone is shallow, so its history cannot be checked; fetch the full history (actions/checkout fetch-depth: 0)" >&2
  exit 1
fi

roots=$(git rev-list --max-parents=0 HEAD)
[ "$roots" = "$root_commit" ] \
  || fail "the history must start at exactly one root commit, $root_commit; HEAD's history starts at: $(echo $roots)"
git cat-file -e "$root_commit^{commit}" 2>/dev/null || { fail "the recorded root commit $root_commit is not in this repository"; root_commit=""; }
if [ -n "$root_commit" ]; then
  got=$(git rev-parse "$root_commit^{tree}")
  [ "$got" = "$root_tree" ] || fail "root commit $root_commit has tree $got, the record says $root_tree"
  got=$(utc_date . "$root_commit")
  [ "$got" = "$root_date" ] || fail "root commit $root_commit was committed on $got (UTC), the record says $root_date"
  git log -1 --format=%B "$root_commit" | grep -qF "$source_commit:$source_path" \
    || fail "root commit $root_commit's message does not name its source as $source_commit:$source_path"
fi
grep -qF "frozen at [\`$source_commit\`]" "$record" || fail "$record's prose does not name the frozen source commit $source_commit"
grep -qF "created on $root_date" "$record" || fail "$record's prose does not say the repository was created on $root_date"

if [ "$check_source" = 1 ] && [ -n "$root_commit" ]; then
  source_repository=$(required source-repository)
  source_date=$(required source-date)
  source_tree=$(required source-tree)
  work=$(mktemp -d)
  trap 'rm -rf "$work"' EXIT
  git init -q --bare "$work/source"
  git -C "$work/source" fetch -q --depth 1 --filter=blob:none "$source_repository" "$source_commit" \
    || { echo "provenance: cannot fetch $source_commit from $source_repository" >&2; exit 1; }

  got=$(utc_date "$work/source" "$source_commit")
  [ "$got" = "$source_date" ] || fail "source commit $source_commit was committed on $got (UTC), the record says $source_date"
  got=$(git -C "$work/source" rev-parse --verify -q "$source_commit:$source_path" || true)
  if [ -z "$got" ]; then
    echo "provenance: $source_commit has no $source_path; $failures other claim(s) failed before this" >&2
    exit 1
  fi
  [ "$got" = "$source_tree" ] || fail "$source_path at $source_commit has tree $got, the record says $source_tree"

  git -C "$work/source" ls-tree -r "$source_commit:$source_path" | awk -F'\t' '{ split($1, m, " "); print $2 "\t" m[1] " " m[3] }' | LC_ALL=C sort >"$work/source.tsv"
  git ls-tree -r "$root_commit" | awk -F'\t' '{ split($1, m, " "); print $2 "\t" m[1] " " m[3] }' | LC_ALL=C sort >"$work/root.tsv"
  LC_ALL=C join -t $'\t' "$work/source.tsv" "$work/root.tsv" >"$work/both.tsv"
  identical=$(awk -F'\t' '$2 == $3' "$work/both.tsv" | wc -l)
  changed=$(awk -F'\t' '$2 != $3' "$work/both.tsv" | wc -l)
  added=$(LC_ALL=C join -t $'\t' -v 2 "$work/source.tsv" "$work/root.tsv" | wc -l)
  removed=$(LC_ALL=C join -t $'\t' -v 1 "$work/source.tsv" "$work/root.tsv" | wc -l)
  for count in identical changed added removed; do
    want=$(required "files-$count")
    [ "${!count}" -eq "$want" ] || fail "the root tree has ${!count} file(s) $count against $source_path, the record says $want"
  done

  read -r -a patterns <<<"$(required byte-identical)"
  for pattern in "${patterns[@]}"; do
    matched=0
    while IFS=$'\t' read -r path source_entry root_entry; do
      [[ $path == $pattern ]] || continue
      matched=$((matched + 1))
      [ "$source_entry" = "$root_entry" ] || fail "$path is recorded byte-identical ($pattern) but differs from $source_path/$path"
    done <"$work/both.tsv"
    while IFS=$'\t' read -r path _; do
      [[ $path == $pattern ]] && fail "$path matches byte-identical pattern $pattern but has no counterpart in $source_path"
    done < <(LC_ALL=C join -t $'\t' -v 2 "$work/source.tsv" "$work/root.tsv")
    [ "$matched" -gt 0 ] || fail "byte-identical pattern $pattern matches no file of the root tree"
  done

  read -r -a copies <<<"$(value copied-unchanged)"
  for path in "${copies[@]}"; do
    got=$(git rev-parse "$root_commit:$path" 2>/dev/null || echo missing)
    want=$(git -C "$work/source" rev-parse "$source_commit:$path" 2>/dev/null || echo missing)
    [ "$got" != missing ] && [ "$got" = "$want" ] || fail "$path in root commit ($got) is recorded as copied unchanged from $source_commit:$path ($want)"
  done
fi

if [ "$failures" -gt 0 ]; then
  echo "provenance: $failures claim(s) in $record do not hold; correct the record or the repository" >&2
  exit 1
fi
if [ "$check_source" = 1 ]; then
  echo "provenance: $record holds for this repository and for $source_commit:$source_path"
else
  echo "provenance: $record holds for this repository (run with --source to check the source side too)"
fi
