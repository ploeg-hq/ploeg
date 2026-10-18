#!/usr/bin/env bash
set -euo pipefail
mode=${1:?expected build, reproduce or publish}
tag=${2:?expected v0.x.y or v0.x.y-rc.N}
source=$(cd "${3:?expected the checkout of the release tag}" && pwd)
output=${CONTRACT_BUNDLE_OUTPUT:-$PWD/dist/contract-bundle}
repository=${CONTRACT_BUNDLE_REPOSITORY:-ghcr.io/ploeg-hq/ploeg-contract}
[[ "$mode" == build || "$mode" == reproduce || "$mode" == publish ]] || { echo "unknown mode $mode" >&2; exit 1; }
[[ "$tag" =~ ^v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$ ]] || { echo "not a Ploeg release tag: $tag" >&2; exit 1; }
[[ "$(gzip --version 2>/dev/null | head -n 1)" =~ ^gzip\ 1\. ]] || { echo 'GNU gzip is required: another gzip compresses the same tar to different bytes' >&2; exit 1; }
pinned_git() {
  git -C "$source" -c tar.umask=0002 -c core.autocrlf=false -c core.eol=lf -c core.attributesFile=/dev/null \
    -c core.quotePath=false -c log.showSignature=false "$@"
}
version=${tag#v}
commit=$(pinned_git rev-parse --verify 'HEAD^{commit}')
tagged=$(pinned_git rev-parse --verify --quiet "refs/tags/$tag^{commit}" || true)
[[ -z "$tagged" || "$tagged" == "$commit" ]] || { echo "$source is at $commit, but $tag is $tagged" >&2; exit 1; }
[[ -z "$(pinned_git status --porcelain --untracked-files=no)" ]] || { echo "$source has local changes; a bundle is built from committed files only" >&2; exit 1; }
local_attributes=$(pinned_git rev-parse --path-format=absolute --git-path info/attributes)
[[ ! -s "$local_attributes" ]] || { echo "$local_attributes changes what git archive writes; remove it to build a bundle" >&2; exit 1; }
mkdir -p "$output"
output=$(cd "$output" && pwd)
name="ploeg-contract-$version.tar.gz"
archive="$output/$name"
layout="$output/oci-layout"
manifest=$(printf '{"schemaVersion":1,"name":"ploeg-contract","version":"%s","commit":"%s","source":"https://github.com/ploeg-hq/ploeg"}' "$version" "$commit")
files=$(pinned_git ls-files -z | tr '\0' '\n' | LC_ALL=C sort)
pinned_git archive --format=tar \
  --add-virtual-file="ploeg-contract.json:$manifest"$'\n' \
  --add-virtual-file="files.txt:$files"$'\n' \
  "$commit" -- docs ':(glob)pkg/**/*.go' ':(glob)cmd/**/*.go' ':(exclude,glob)**/*_test.go' \
  | gzip -n -9 > "$archive"
layer=$(sha256sum "$archive" | cut -d' ' -f1)
created=$(pinned_git show -s --format=%cI "$commit")
rm -rf "$layout"
(cd "$output" && oras push --oci-layout "$layout:$version" \
  --artifact-type application/vnd.ploeg.contract.v1+json \
  --annotation "org.opencontainers.image.source=https://github.com/ploeg-hq/ploeg" \
  --annotation "org.opencontainers.image.revision=$commit" \
  --annotation "org.opencontainers.image.version=$version" \
  --annotation "org.opencontainers.image.created=$created" \
  --annotation "org.opencontainers.image.licenses=Apache-2.0" \
  "$name:application/vnd.ploeg.contract.layer.v1.tar+gzip" > /dev/null)
digest=$(oras resolve --oci-layout "$layout:$version")
reference="$repository:$version"
echo "Built $reference@$digest (layer sha256:$layer) from $commit into $output"
[[ "$mode" != build ]] || exit 0
if [[ "$mode" == publish ]]; then
  : "${GITHUB_REPOSITORY:?}"
  : "${GITHUB_ACTOR:?}"
  : "${GH_TOKEN:?}"
  [[ "$GITHUB_REPOSITORY" == ploeg-hq/ploeg ]] || exit 1
  printf '%s' "$GH_TOKEN" | oras login "${repository%%/*}" --username "$GITHUB_ACTOR" --password-stdin
fi
if published=$(oras resolve "$reference" 2>"$output/registry-check.txt"); then
  if [[ "$published" == "$digest" ]]; then
    echo "$reference is $published, the digest this checkout builds"
    exit 0
  fi
  echo "$reference is $published, but this checkout builds $digest" >&2
  oras manifest fetch "$reference" >&2 || true
  echo >&2
  oras manifest fetch --oci-layout "$layout:$version" >&2
  echo >&2
  exit 1
fi
if [[ "$mode" == reproduce ]]; then
  echo "Could not resolve $reference to compare against" >&2
  cat "$output/registry-check.txt" >&2
  exit 1
fi
grep -Eqi 'not found|manifest unknown|name unknown' "$output/registry-check.txt" || {
  echo "Registry absence not confirmed for $reference; refusing publication" >&2
  cat "$output/registry-check.txt" >&2
  exit 1
}
oras cp --from-oci-layout "$layout:$version" "$reference"
pushed=$(oras resolve "$reference")
[[ "$pushed" == "$digest" ]] || { echo "$reference resolved to $pushed after the copy, not $digest" >&2; exit 1; }
echo "Published $reference@$digest"
[[ -z "${GITHUB_STEP_SUMMARY:-}" ]] || printf 'Contract bundle: `%s@%s`\n' "$reference" "$digest" >> "$GITHUB_STEP_SUMMARY"
