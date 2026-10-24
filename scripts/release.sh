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
chart="ghcr.io/ploeg-hq/charts/ploeg:$version"
mkdir -p dist/bin dist/cve
go build -o dist/bin/cve-gate ./internal/cvegate/cmd/cve-gate
declare -A repositories dockerfiles ships_go digests
names=()
while read -r name repository dockerfile go_binaries; do
  names+=("$name")
  repositories[$name]=$repository
  dockerfiles[$name]=$dockerfile
  ships_go[$name]=$go_binaries
done < <(dist/bin/cve-gate images)
[[ "${#names[@]}" -gt 0 ]] || { echo "The image catalogue lists no image" >&2; exit 1; }
printf '%s' "$GH_TOKEN" | docker login ghcr.io -u "$GITHUB_ACTOR" --password-stdin
for artifact in "$chart" $(for name in "${names[@]}"; do echo "${repositories[$name]}:$version"; done); do
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
created=$(git show -s --format=%cI HEAD)
metadata_file() { if [[ "$1" == ploegd ]]; then echo dist/image-metadata.json; else echo "dist/$1-image-metadata.json"; fi; }
docker buildx create --name ploeg-release --driver docker-container --use
for name in "${names[@]}"; do
  repository=${repositories[$name]}
  docker buildx build --file "${dockerfiles[$name]}" --platform linux/amd64 \
    --build-arg "IMAGE_VERSION=$version" --build-arg "IMAGE_REVISION=$commit" --build-arg "IMAGE_CREATED=$created" \
    --label "org.opencontainers.image.version=$version" --label "org.opencontainers.image.revision=$commit" \
    --provenance=mode=max --sbom=true --metadata-file "$(metadata_file "$name")" \
    --output "type=image,name=$repository,push-by-digest=true,name-canonical=true,push=true" .
  digest=$(jq -r '."containerimage.digest"' "$(metadata_file "$name")")
  [[ "$digest" =~ ^sha256:[a-f0-9]{64}$ ]] || { echo "No digest resolved for $repository; refusing to publish" >&2; exit 1; }
  if ! IMAGE_REF="$repository@$digest" IMAGE_NAME="$name" OUT_DIR="dist/cve/$name" dist/bin/cve-gate gate; then
    echo "$repository@$digest is over its CVE budget or has no verdict; it stays untagged and unsigned" >&2; exit 1
  fi
  if [[ "${ships_go[$name]}" == true ]]; then
    docker pull --quiet "$repository@$digest" > /dev/null
    bash scripts/check-go-toolchain.sh "$repository@$digest"
  fi
  digests[$name]=$digest
done
for name in "${names[@]}"; do
  oras tag "${repositories[$name]}@${digests[$name]}" "$version"
done
image_digest=${digests[ploegd]}
runner_digest=${digests[claude-runner]}
worker_digest=${digests[ploeg-worker]}
bash scripts/package-chart.sh "$version" "$image_digest" "$runner_digest" "$worker_digest" dist
printf '%s' "$GH_TOKEN" | helm registry login ghcr.io --username "$GITHUB_ACTOR" --password-stdin
helm push "dist/ploeg-$version.tgz" oci://ghcr.io/ploeg-hq/charts 2>&1 | tee dist/chart-push.txt
chart_digest=$(sed -n 's/^Digest: *//p' dist/chart-push.txt)
signed_artifacts=("ghcr.io/ploeg-hq/charts/ploeg@$chart_digest")
for name in "${names[@]}"; do signed_artifacts+=("${repositories[$name]}@${digests[$name]}"); done
signer_identity="^https://github\.com/ploeg-hq/ploeg/\.github/workflows/release\.yml@refs/(heads/development|heads/main|tags/v0\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?)$"
for artifact in "${signed_artifacts[@]}"; do
  [[ "$artifact" =~ @sha256:[a-f0-9]{64}$ ]] || { echo "No digest resolved for $artifact; refusing to publish unsigned" >&2; exit 1; }
  cosign sign --yes "$artifact"
  cosign verify --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    --certificate-identity-regexp "$signer_identity" "$artifact" > /dev/null
done
attestation_assets=()
for name in "${names[@]}"; do
  subject="${repositories[$name]}@${digests[$name]}"
  IMAGE_NAME="$name" IMAGE_DIGEST="${digests[$name]}" VEX_TIMESTAMP="$created" \
    VEX_ID="https://github.com/$GITHUB_REPOSITORY/releases/download/$tag/$name.openvex.json" \
    dist/bin/cve-gate release-vex > "dist/$name.openvex.json"
  cosign attest --yes --type openvex --predicate "dist/$name.openvex.json" "$subject"
  cosign verify-attestation --type openvex --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    --certificate-identity-regexp "$signer_identity" "$subject" > /dev/null
  cp "dist/cve/$name/cve-budget.json" "dist/$name.cve-budget.json"
  jq -e --arg subject "$subject" '.image == $subject and .verdict == "pass" and .mode == "enforce"' "dist/$name.cve-budget.json" > /dev/null \
    || { echo "The CVE budget verdict of $name does not pass $subject under enforcement" >&2; exit 1; }
  cosign attest --yes --type https://github.com/ploeg-hq/ploeg/attestations/cve-budget/v1 --predicate "dist/$name.cve-budget.json" "$subject"
  cosign verify-attestation --type https://github.com/ploeg-hq/ploeg/attestations/cve-budget/v1 --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    --certificate-identity-regexp "$signer_identity" "$subject" > /dev/null
  attestation_assets+=("$name.openvex.json" "$name.cve-budget.json")
done
printf '%s\n' "${signed_artifacts[@]}" > dist/signed-artifacts.txt
printf '%s\n' "$commit" > dist/source-commit.txt
cp PROVENANCE.md dist/PROVENANCE.md
printf 'image:\n  repository: ghcr.io/ploeg-hq/ploegd\n  tag: %s\n' "$version" > dist/registry-values.yaml
(cd dist && sha256sum "ploeg_${version}_linux_amd64.tar.gz" "ploeg-$version.tgz" image-metadata.json claude-runner-image-metadata.json ploeg-worker-image-metadata.json signed-artifacts.txt source-commit.txt PROVENANCE.md registry-values.yaml "${attestation_assets[@]}" > SHA256SUMS)
printf 'Ploeg %s\n\nIndependent release from commit %s. Experimental: qualification is limited to tested paths.\n\nController and worker: %s (Linux amd64). Self-hosted Claude runner and dispatch image (ADR-0084): ghcr.io/ploeg-hq/claude-runner:%s. Worker image, the worker container of a Run whose harness runs in its own container (ADR-0138): ghcr.io/ploeg-hq/ploeg-worker:%s. Helm chart: oci://ghcr.io/ploeg-hq/charts/ploeg, version %s. BuildKit publishes image SBOM and build provenance attestations. Every image passed the CVE gate (zero Critical and High findings after reviewed OpenVEX statements, ops/security/cve-budgets.yaml) before it was tagged, and carries its OpenVEX document and its CVE budget verdict as signed cosign attestations (verify with cosign verify-attestation --type openvex, or --type https://github.com/ploeg-hq/ploeg/attestations/cve-budget/v1); the same documents are the <image>.openvex.json and <image>.cve-budget.json release assets, for grype --vex and trivy --vex. Every image and the chart are signed keyless with cosign by the ploeg-hq/ploeg release workflow (GitHub Actions OIDC, public Rekor log); verify with cosign verify --certificate-oidc-issuer https://token.actions.githubusercontent.com --certificate-identity-regexp '^https://github.com/ploeg-hq/ploeg/.github/workflows/release.yml@'. Release assets include binaries, chart, checksums, the signed digests and source provenance.\n\nThis starts the new github.com/ploeg-hq/ploeg module; old webgrip/ploeg versions remain unchanged. No latest alias or deployment is updated. See PROVENANCE.md and the managed-worker migration guide before adoption.\n' "$version" "$commit" "ghcr.io/ploeg-hq/ploegd:$version" "$version" "$version" "$version" > dist/release-notes.md
assets=("dist/ploeg_${version}_linux_amd64.tar.gz" "dist/ploeg-$version.tgz" dist/image-metadata.json dist/claude-runner-image-metadata.json dist/ploeg-worker-image-metadata.json dist/signed-artifacts.txt dist/source-commit.txt dist/PROVENANCE.md dist/registry-values.yaml "${attestation_assets[@]/#/dist/}" dist/SHA256SUMS)
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
