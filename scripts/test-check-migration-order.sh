#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
cd "$fixture"
git init --quiet -b development
git config user.name 'Migration order test'
git config user.email migration-order-test@example.invalid
mkdir -p pkg/store/migrations
for name in 0001_init 0036_legacy_a 0036_legacy_b 0040_tasks 0042_runs; do
  : >"pkg/store/migrations/$name.sql"
done
git add pkg && git commit --quiet -m base
base=$(git rev-parse HEAD)

refuses() {
  local case=$1
  shift
  local status=0
  bash "$root/scripts/check-migration-order.sh" "$@" >/dev/null 2>&1 || status=$?
  if [ "$status" -ne "${expected:-1}" ]; then
    echo "check-migration-order answered $status, not ${expected:-1}, for $case" >&2
    exit 1
  fi
}

bash "$root/scripts/check-migration-order.sh" "$base" >/dev/null

: >pkg/store/migrations/0043_next.sql
bash "$root/scripts/check-migration-order.sh" "$base" >/dev/null
git add pkg && git commit --quiet -m next
bash "$root/scripts/check-migration-order.sh" "$base" HEAD >/dev/null

: >pkg/store/migrations/0041_between.sql
refuses 'a migration numbered below the base branch highest' "$base"
rm pkg/store/migrations/0041_between.sql

mv pkg/store/migrations/0042_runs.sql pkg/store/migrations/0042_renamed.sql
refuses 'a migration numbered at the base branch highest' "$base"
mv pkg/store/migrations/0042_renamed.sql pkg/store/migrations/0042_runs.sql

: >pkg/store/migrations/0043_twin.sql
refuses 'two migrations with one number above the legacy duplicate' "$base"
rm pkg/store/migrations/0043_twin.sql

git checkout --quiet "$base"
: >pkg/store/migrations/0040_rebased.sql
git add pkg && git commit --quiet -m stale
refuses 'a committed migration below the base branch highest' "$base" HEAD

expected=2 refuses 'a base that is not a commit' not-a-commit
