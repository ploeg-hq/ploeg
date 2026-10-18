#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
repository=${CONTRACT_BUNDLE_REPOSITORY:-ghcr.io/ploeg-hq/ploeg-contract}
unpublished_tags_to_skip=${CONTRACT_BUNDLE_UNPUBLISHED_SKIP:-3}
work=$(mktemp -d)
trap 'git -C "$root" worktree remove --force "$work/reference" 2>/dev/null || true; rm -rf "$work"' EXIT
start=HEAD
for _ in $(seq 0 "$unpublished_tags_to_skip"); do
  tag=$(git -C "$root" describe --tags --match 'v*' --abbrev=0 "$start" 2>/dev/null) || { echo "no published v* tag is an ancestor of HEAD" >&2; exit 1; }
  if oras resolve "$repository:${tag#v}" > /dev/null 2> "$work/resolve.txt"; then
    git -C "$root" worktree add --quiet --detach "$work/reference" "refs/tags/$tag"
    CONTRACT_BUNDLE_OUTPUT="$work/bundle" CONTRACT_BUNDLE_REPOSITORY="$repository" \
      bash "$root/scripts/contract-bundle.sh" reproduce "$tag" "$work/reference"
    exit
  fi
  grep -Eqi 'not found|manifest unknown|name unknown' "$work/resolve.txt" || { cat "$work/resolve.txt" >&2; exit 1; }
  echo "$tag has no published contract bundle yet; trying the tag before it"
  start="$tag^"
done
echo "none of the newest $((unpublished_tags_to_skip + 1)) tags before HEAD has a published contract bundle" >&2
exit 1
