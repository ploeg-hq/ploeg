#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
cd "$fixture"
git init --quiet -b development
git config user.name 'DCO test'
git config user.email dco-test@example.invalid
git commit --quiet --allow-empty -m base
base=$(git rev-parse HEAD)
git commit --quiet --allow-empty -s -m signed
bash "$root/scripts/dco-check.sh" "$base" HEAD
git -c user.email=renovate@example.invalid -c 'user.name=bot' commit --quiet --allow-empty --author='ploeg-hq-renovate[bot] <344652002+ploeg-hq-renovate[bot]@users.noreply.github.com>' -m 'chore(deps): bot'
bash "$root/scripts/dco-check.sh" "$base" HEAD
git commit --quiet --allow-empty -m 'signed by another' -m 'Signed-off-by: Someone Else <else@example.invalid>'
if bash "$root/scripts/dco-check.sh" "$base" HEAD 2>/dev/null; then echo 'accepted a sign-off by someone other than the author' >&2; exit 1; fi
git reset --quiet --hard HEAD~1
git commit --quiet --allow-empty -m unsigned
if bash "$root/scripts/dco-check.sh" "$base" HEAD 2>/dev/null; then echo 'accepted an unsigned commit' >&2; exit 1; fi
