#!/usr/bin/env bash
set -euo pipefail
mode=${1:?expected build or publish}
tag=${2:?expected v0.x.y or v0.x.y-rc.N}
source=$(cd "${3:?expected the checkout of the release tag}" && pwd)
output=${CONTRACT_BUNDLE_OUTPUT:-$PWD/dist/contract-bundle}
[[ "$mode" == build || "$mode" == publish ]] || { echo "unknown mode $mode" >&2; exit 1; }
[[ "$tag" =~ ^v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$ ]] || { echo "not a Ploeg release tag: $tag" >&2; exit 1; }
version=${tag#v}
commit=$(git -C "$source" rev-parse --verify 'HEAD^{commit}')
tagged=$(git -C "$source" rev-parse --verify --quiet "refs/tags/$tag^{commit}" || true)
[[ -z "$tagged" || "$tagged" == "$commit" ]] || { echo "$source is at $commit, but $tag is $tagged" >&2; exit 1; }
[[ -z "$(git -C "$source" status --porcelain --untracked-files=no)" ]] || { echo "$source has local changes; a bundle is built from committed files only" >&2; exit 1; }
mkdir -p "$output"
output=$(cd "$output" && pwd)
archive="$output/ploeg-contract-$version.tar.gz"
manifest=$(printf '{"schemaVersion":1,"name":"ploeg-contract","version":"%s","commit":"%s","source":"https://github.com/ploeg-hq/ploeg"}' "$version" "$commit")
git -C "$source" ls-files -z | tr '\0' '\n' | LC_ALL=C sort > "$output/files.txt"
git -C "$source" -c core.quotePath=false archive --format=tar \
  --add-virtual-file="ploeg-contract.json:$manifest"$'\n' \
  --add-virtual-file="files.txt:$(cat "$output/files.txt")"$'\n' \
  "$commit" -- docs ':(glob)pkg/**/*.go' ':(glob)cmd/**/*.go' ':(exclude,glob)**/*_test.go' \
  | gzip -n -9 > "$archive"
rm "$output/files.txt"
layer=$(sha256sum "$archive" | cut -d' ' -f1)
echo "Built $archive (sha256:$layer) from $commit"
[[ "$mode" == publish ]] || exit 0
: "${GITHUB_REPOSITORY:?}"
: "${GITHUB_ACTOR:?}"
: "${GH_TOKEN:?}"
[[ "$GITHUB_REPOSITORY" == ploeg-hq/ploeg ]] || exit 1
reference="ghcr.io/ploeg-hq/ploeg-contract:$version"
printf '%s' "$GH_TOKEN" | oras login ghcr.io --username "$GITHUB_ACTOR" --password-stdin
if published=$(oras manifest fetch "$reference" 2>"$output/registry-check.txt"); then
  if grep -q "\"sha256:$layer\"" <<<"$published"; then
    echo "$reference already carries this bundle ($(oras resolve "$reference"))"
    exit 0
  fi
  echo "Refusing to overwrite $reference: it carries a different bundle" >&2
  exit 1
fi
grep -Eqi 'not found|manifest unknown|name unknown' "$output/registry-check.txt" || {
  echo "Registry absence not confirmed for $reference; refusing publication" >&2
  cat "$output/registry-check.txt" >&2
  exit 1
}
created=$(git -C "$source" show -s --format=%cI "$commit")
(cd "$output" && oras push "$reference" \
  --artifact-type application/vnd.ploeg.contract.v1+json \
  --annotation "org.opencontainers.image.source=https://github.com/ploeg-hq/ploeg" \
  --annotation "org.opencontainers.image.revision=$commit" \
  --annotation "org.opencontainers.image.version=$version" \
  --annotation "org.opencontainers.image.created=$created" \
  --annotation "org.opencontainers.image.licenses=Apache-2.0" \
  "ploeg-contract-$version.tar.gz:application/vnd.ploeg.contract.layer.v1.tar+gzip")
digest=$(oras resolve "$reference")
echo "Published $reference@$digest"
[[ -z "${GITHUB_STEP_SUMMARY:-}" ]] || printf 'Contract bundle: `%s@%s`\n' "$reference" "$digest" >> "$GITHUB_STEP_SUMMARY"
