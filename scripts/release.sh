#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mode=${1:?expected validate or publish}
tag=${2:?expected v0.x.y or v0.x.y-rc.N}
[[ "$mode" == validate || "$mode" == publish ]]
[[ "$tag" =~ ^v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$ ]]
version=${tag#v}
commit=$(git rev-parse --verify "refs/tags/$tag^{commit}")
if [[ "$tag" == *-rc.* ]]; then
  git merge-base --is-ancestor "$commit" origin/development 2>/dev/null || git merge-base --is-ancestor "$commit" origin/main
else
  git merge-base --is-ancestor "$commit" origin/main
fi
[[ "$(git rev-parse HEAD)" == "$commit" ]]
[[ "$(sed -n 's/^version: *//p' ops/helm/ploeg/Chart.yaml)" == "$version" ]]
[[ "$(sed -n 's/^appVersion: *//p' ops/helm/ploeg/Chart.yaml)" == "$version" ]]
[[ "$mode" == publish ]] || exit 0
: "${GITHUB_REPOSITORY:?}"
: "${GITHUB_ACTOR:?}"
: "${GH_TOKEN:?}"
[[ "$GITHUB_REPOSITORY" == ploeg-hq/ploeg ]]
mkdir -p dist
status=$(curl --silent --show-error --output dist/release-check.json --write-out '%{http_code}' --header "Authorization: Bearer $GH_TOKEN" "https://api.github.com/repos/$GITHUB_REPOSITORY/releases/tags/$tag")
[[ "$status" == 404 ]] || { echo "Release absence not confirmed (HTTP $status); refusing overwrite" >&2; exit 1; }
image="ghcr.io/ploeg-hq/ploegd:$version"
chart="ghcr.io/ploeg-hq/charts/ploeg:$version"
printf '%s' "$GH_TOKEN" | docker login ghcr.io -u "$GITHUB_ACTOR" --password-stdin
for artifact in "$image" "$chart"; do
  if docker manifest inspect "$artifact" > /dev/null 2>dist/registry-check.txt; then
    echo "Refusing to overwrite $artifact" >&2; exit 1
  fi
  if ! grep -Eqi 'manifest unknown|no such manifest|name unknown' dist/registry-check.txt; then
    echo "Registry absence not confirmed for $artifact; refusing publication" >&2
    cat dist/registry-check.txt >&2; exit 1
  fi
done
for binary in ploegd ploeg-worker; do
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=$version" -o "dist/$binary" "./cmd/$binary"
done
tar -czf "dist/ploeg_${version}_linux_amd64.tar.gz" -C dist ploegd ploeg-worker
rm dist/ploegd dist/ploeg-worker
docker buildx create --name ploeg-release --driver docker-container --use
docker buildx build --file ops/docker/ploegd/Dockerfile --platform linux/amd64 \
  --build-arg "IMAGE_VERSION=$version" --build-arg "IMAGE_REVISION=$commit" \
  --build-arg "IMAGE_CREATED=$(git show -s --format=%cI HEAD)" \
  --provenance=mode=max --sbom=true --metadata-file dist/image-metadata.json \
  --tag "$image" --push .
helm package ops/helm/ploeg --destination dist
printf '%s' "$GH_TOKEN" | helm registry login ghcr.io --username "$GITHUB_ACTOR" --password-stdin
helm push "dist/ploeg-$version.tgz" oci://ghcr.io/ploeg-hq/charts
printf '%s\n' "$commit" > dist/source-commit.txt
cp PROVENANCE.md dist/PROVENANCE.md
printf 'image:\n  repository: ghcr.io/ploeg-hq/ploegd\n  tag: %s\n' "$version" > dist/registry-values.yaml
(cd dist && sha256sum "ploeg_${version}_linux_amd64.tar.gz" "ploeg-$version.tgz" image-metadata.json source-commit.txt PROVENANCE.md registry-values.yaml > SHA256SUMS)
printf 'Ploeg %s\n\nIndependent release from commit %s. Experimental: qualification is limited to tested paths.\n\nController and worker: %s (Linux amd64). Helm chart: oci://ghcr.io/ploeg-hq/charts/ploeg, version %s. BuildKit publishes image SBOM and build provenance attestations. Release assets include binaries, chart, checksums and source provenance. Cryptographic release signing is not yet configured.\n\nThis starts the new github.com/ploeg-hq/ploeg module; old webgrip/ploeg versions remain unchanged. No latest alias or deployment is updated. See PROVENANCE.md and the managed-worker migration guide before adoption.\n' "$version" "$commit" "$image" "$version" > dist/release-notes.md
args=()
[[ "$tag" != *-rc.* ]] || args+=(--prerelease)
gh release create "$tag" "dist/ploeg_${version}_linux_amd64.tar.gz" "dist/ploeg-$version.tgz" dist/image-metadata.json dist/source-commit.txt dist/PROVENANCE.md dist/registry-values.yaml dist/SHA256SUMS --repo "$GITHUB_REPOSITORY" --verify-tag --latest=false "${args[@]}" --title "Ploeg $version" --notes-file dist/release-notes.md
