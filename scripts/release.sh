#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mode=${1:?expected check, validate or publish}
tag=${2:?expected v0.x.y or v0.x.y-rc.N}
[[ "$mode" == check || "$mode" == validate || "$mode" == publish ]] || exit 1
[[ "$tag" =~ ^v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$ ]] || { echo "$tag is not v0.x.y or v0.x.y-rc.N" >&2; exit 1; }
version=${tag#v}
if [[ "$mode" == check ]]; then
  [[ "$tag" == *-rc.* ]] || { echo "Automatic releases are release candidates only; $tag needs a reviewed stable release" >&2; exit 1; }
  if git rev-parse --verify --quiet "refs/tags/$tag" > /dev/null; then echo "$tag already exists" >&2; exit 1; fi
  exit 0
fi
commit=$(git rev-parse --verify "refs/tags/$tag^{commit}")
if [[ "$tag" == *-rc.* ]]; then
  git merge-base --is-ancestor "$commit" origin/development 2>/dev/null || git merge-base --is-ancestor "$commit" origin/main
else
  git merge-base --is-ancestor "$commit" origin/main
fi
[[ "$(git rev-parse HEAD)" == "$commit" ]] || exit 1
if [[ "$tag" != *-rc.* ]]; then
  [[ "$(sed -n 's/^version: *//p' ops/helm/ploeg/Chart.yaml)" == "$version" ]] || { echo "Chart.yaml version does not match the stable tag $tag" >&2; exit 1; }
  [[ "$(sed -n 's/^appVersion: *//p' ops/helm/ploeg/Chart.yaml)" == "$version" ]] || { echo "Chart.yaml appVersion does not match the stable tag $tag" >&2; exit 1; }
fi
[[ "$mode" == publish ]] || exit 0
: "${GITHUB_REPOSITORY:?}"
: "${GITHUB_ACTOR:?}"
: "${GH_TOKEN:?}"
[[ "$GITHUB_REPOSITORY" == ploeg-hq/ploeg ]] || exit 1
mkdir -p dist
status=$(curl --silent --show-error --output dist/release-check.json --write-out '%{http_code}' --header "Authorization: Bearer $GH_TOKEN" "https://api.github.com/repos/$GITHUB_REPOSITORY/releases/tags/$tag")
[[ "$status" == 404 ]] || { echo "Release absence not confirmed (HTTP $status); refusing overwrite" >&2; exit 1; }
draft=$(gh api "repos/$GITHUB_REPOSITORY/releases?per_page=100" --jq "[.[] | select(.draft and .tag_name == \"$tag\")] | if length == 0 then \"none\" elif length == 1 and (.[0].assets | length) == 0 then (.[0].id | tostring) else \"refused\" end")
[[ "$draft" =~ ^(none|[0-9]+)$ ]] || { echo "The draft release for $tag already carries assets or is duplicated; refusing overwrite" >&2; exit 1; }
image="ghcr.io/ploeg-hq/ploegd:$version"
chart="ghcr.io/ploeg-hq/charts/ploeg:$version"
runner_image="ghcr.io/ploeg-hq/claude-runner:$version"
worker_image="ghcr.io/ploeg-hq/ploeg-worker:$version"
printf '%s' "$GH_TOKEN" | docker login ghcr.io -u "$GITHUB_ACTOR" --password-stdin
for artifact in "$image" "$chart" "$runner_image" "$worker_image"; do
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
notices=(LICENSE NOTICE THIRD-PARTY-NOTICES)
cp "${notices[@]}" dist/
tar -czf "dist/ploeg_${version}_linux_amd64.tar.gz" -C dist ploegd ploeg-worker "${notices[@]}"
rm dist/ploegd dist/ploeg-worker "${notices[@]/#/dist/}"
docker buildx create --name ploeg-release --driver docker-container --use
docker buildx build --file ops/docker/ploegd/Dockerfile --platform linux/amd64 \
  --build-arg "IMAGE_VERSION=$version" --build-arg "IMAGE_REVISION=$commit" \
  --build-arg "IMAGE_CREATED=$(git show -s --format=%cI HEAD)" \
  --provenance=mode=max --sbom=true --metadata-file dist/image-metadata.json \
  --tag "$image" --push .
docker buildx build --file ops/claude-runner/Dockerfile --platform linux/amd64 \
  --label "org.opencontainers.image.version=$version" --label "org.opencontainers.image.revision=$commit" \
  --provenance=mode=max --sbom=true --metadata-file dist/claude-runner-image-metadata.json \
  --tag "$runner_image" --push .
docker buildx build --file ops/docker/ploeg-worker/Dockerfile --platform linux/amd64 \
  --build-arg "IMAGE_VERSION=$version" --build-arg "IMAGE_REVISION=$commit" \
  --build-arg "IMAGE_CREATED=$(git show -s --format=%cI HEAD)" \
  --provenance=mode=max --sbom=true --metadata-file dist/ploeg-worker-image-metadata.json \
  --tag "$worker_image" --push .
image_digest=$(jq -r '."containerimage.digest"' dist/image-metadata.json)
runner_digest=$(jq -r '."containerimage.digest"' dist/claude-runner-image-metadata.json)
worker_digest=$(jq -r '."containerimage.digest"' dist/ploeg-worker-image-metadata.json)
bash scripts/package-chart.sh "$version" "$image_digest" "$runner_digest" "$worker_digest" dist
printf '%s' "$GH_TOKEN" | helm registry login ghcr.io --username "$GITHUB_ACTOR" --password-stdin
helm push "dist/ploeg-$version.tgz" oci://ghcr.io/ploeg-hq/charts 2>&1 | tee dist/chart-push.txt
chart_digest=$(sed -n 's/^Digest: *//p' dist/chart-push.txt)
signed_artifacts=("ghcr.io/ploeg-hq/ploegd@$image_digest" "ghcr.io/ploeg-hq/claude-runner@$runner_digest" "ghcr.io/ploeg-hq/ploeg-worker@$worker_digest" "ghcr.io/ploeg-hq/charts/ploeg@$chart_digest")
signer_identity="^https://github\.com/ploeg-hq/ploeg/\.github/workflows/release\.yml@refs/(heads/development|heads/main|tags/v0\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?)$"
for artifact in "${signed_artifacts[@]}"; do
  [[ "$artifact" =~ @sha256:[a-f0-9]{64}$ ]] || { echo "No digest resolved for $artifact; refusing to publish unsigned" >&2; exit 1; }
  cosign sign --yes "$artifact"
  cosign verify --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    --certificate-identity-regexp "$signer_identity" "$artifact" > /dev/null
done
printf '%s\n' "${signed_artifacts[@]}" > dist/signed-artifacts.txt
printf '%s\n' "$commit" > dist/source-commit.txt
cp PROVENANCE.md dist/PROVENANCE.md
printf 'image:\n  repository: ghcr.io/ploeg-hq/ploegd\n  tag: %s\n' "$version" > dist/registry-values.yaml
(cd dist && sha256sum "ploeg_${version}_linux_amd64.tar.gz" "ploeg-$version.tgz" image-metadata.json claude-runner-image-metadata.json ploeg-worker-image-metadata.json signed-artifacts.txt source-commit.txt PROVENANCE.md registry-values.yaml > SHA256SUMS)
printf 'Ploeg %s\n\nIndependent release from commit %s. Experimental: qualification is limited to tested paths.\n\nController and worker: %s (Linux amd64). Self-hosted Claude runner and dispatch image (ADR-0084): ghcr.io/ploeg-hq/claude-runner:%s. Worker image, the worker container of a Run whose harness runs in its own container (ADR-0138): ghcr.io/ploeg-hq/ploeg-worker:%s. Helm chart: oci://ghcr.io/ploeg-hq/charts/ploeg, version %s. BuildKit publishes image SBOM and build provenance attestations. Every image and the chart are signed keyless with cosign by the ploeg-hq/ploeg release workflow (GitHub Actions OIDC, public Rekor log); verify with cosign verify --certificate-oidc-issuer https://token.actions.githubusercontent.com --certificate-identity-regexp '^https://github.com/ploeg-hq/ploeg/.github/workflows/release.yml@'. Release assets include binaries, chart, checksums, the signed digests and source provenance.\n\nThis starts the new github.com/ploeg-hq/ploeg module; old webgrip/ploeg versions remain unchanged. No latest alias or deployment is updated. See PROVENANCE.md and the managed-worker migration guide before adoption.\n' "$version" "$commit" "$image" "$version" "$version" "$version" > dist/release-notes.md
assets=("dist/ploeg_${version}_linux_amd64.tar.gz" "dist/ploeg-$version.tgz" dist/image-metadata.json dist/claude-runner-image-metadata.json dist/ploeg-worker-image-metadata.json dist/signed-artifacts.txt dist/source-commit.txt dist/PROVENANCE.md dist/registry-values.yaml dist/SHA256SUMS)
prerelease=false
[[ "$tag" != *-rc.* ]] || prerelease=true
if [[ "$draft" == none ]]; then
  gh release create "$tag" "${assets[@]}" --repo "$GITHUB_REPOSITORY" --verify-tag --latest=false --prerelease="$prerelease" --title "Ploeg $version" --notes-file dist/release-notes.md
  exit 0
fi
gh api "repos/$GITHUB_REPOSITORY/releases/$draft" --jq .body > dist/changelog-notes.md
printf '\n' >> dist/release-notes.md
cat dist/changelog-notes.md >> dist/release-notes.md
gh release upload "$tag" "${assets[@]}" --repo "$GITHUB_REPOSITORY"
gh release edit "$tag" --repo "$GITHUB_REPOSITORY" --verify-tag --latest=false --prerelease="$prerelease" --title "Ploeg $version" --notes-file dist/release-notes.md --draft=false
