#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
ci="$root/ops/helm/ploeg/ci"
ploegd_digest="sha256:$(printf ploegd | sha256sum | cut -d' ' -f1)"
runner_digest="sha256:$(printf claude-runner | sha256sum | cut -d' ' -f1)"
worker_digest="sha256:$(printf ploeg-worker | sha256sum | cut -d' ' -f1)"
fail() { echo "$1" >&2; exit 1; }

if bash "$root/scripts/package-chart.sh" 9.9.9 sha256:short "$runner_digest" "$worker_digest" "$work/refused" > /dev/null 2>&1; then
  fail 'packaged a chart without a valid ploegd digest'
fi
if bash "$root/scripts/package-chart.sh" 9.9.9 "$ploegd_digest" "$runner_digest" sha256:short "$work/refused" > /dev/null 2>&1; then
  fail 'packaged a chart without a valid ploeg-worker digest'
fi
[[ ! -e "$work/refused" ]] || fail 'left a chart behind after refusing to package'

source_values=$(sha256sum < "$root/ops/helm/ploeg/values.yaml")
bash "$root/scripts/package-chart.sh" 9.9.9 "$ploegd_digest" "$runner_digest" "$worker_digest" "$work" > /dev/null
chart="$work/ploeg-9.9.9.tgz"
[[ -f "$chart" ]] || fail "no chart at $chart"
tar -xOf "$chart" ploeg/SECURITY.md | cmp - "$root/docs/ops/self-hosting-security.md" || fail 'release security guide does not match its source'
tar -xOf "$chart" ploeg/security-contract.json | cmp - "$root/ops/helm/ploeg/security-contract.json" || fail 'release security contract does not match its source'
[[ "$(sha256sum < "$root/ops/helm/ploeg/values.yaml")" == "$source_values" ]] || fail 'packaging changed the source values.yaml'

assert_every_image_pinned() { # <label> <helm template args...>
  local label=$1; shift
  local rendered images
  rendered=$(helm template ploeg "$chart" "$@")
  images=$(printf '%s\n' "$rendered" | sed -n 's/^ *image: *//p')
  [[ -n "$images" ]] || fail "$label rendered no image"
  if printf '%s\n' "$images" | grep -v '@sha256:[a-f0-9]\{64\}$'; then
    fail "$label renders the images above without a digest"
  fi
}
assert_every_image_pinned 'the published chart with only the required values' -f "$ci/required-values.yaml"
assert_every_image_pinned 'the published chart with ploeg-mcp, claude-runner and a claude-environment team' \
  -f "$ci/required-values.yaml" -f "$ci/mcp-values.yaml" -f "$ci/claude-runner-values.yaml"
assert_every_image_pinned 'the published chart with a worker whose harness runs in its own container' \
  -f "$ci/required-values.yaml" -f "$ci/executor-default-worker-values.yaml" --set executor.harness.image=registry.example/agent-runner:1.3.0@sha256:0000000000000000000000000000000000000000000000000000000000000000

rendered=$(helm template ploeg "$chart" -f "$ci/required-values.yaml" -f "$ci/mcp-values.yaml" -f "$ci/claude-runner-values.yaml")
grep -qF "image: ghcr.io/ploeg-hq/ploegd:9.9.9@$ploegd_digest" <<< "$rendered" || fail 'ploegd does not default to the release digest'
grep -qF "image: ghcr.io/ploeg-hq/claude-runner:9.9.9@$runner_digest" <<< "$rendered" || fail 'claude-runner does not default to the release digest'
rendered=$(helm template ploeg "$chart" -f "$ci/required-values.yaml" -f "$ci/executor-default-worker-values.yaml")
grep -qF "image: ghcr.io/ploeg-hq/ploeg-worker:9.9.9@$worker_digest" <<< "$rendered" || fail 'the worker container does not default to the ploeg-worker release digest'

rendered=$(helm template ploeg "$chart" -f "$ci/required-values.yaml" --set image.tag=1.2.3 --set image.repository=registry.example/ploegd)
grep -qF 'image: registry.example/ploegd:1.2.3' <<< "$rendered" || fail 'a tag the cluster sets does not win'
if grep -qF "$ploegd_digest" <<< "$rendered"; then fail 'the release digest is applied to a tag the cluster set'; fi
rendered=$(helm template ploeg "$chart" -f "$ci/required-values.yaml" --set "image.tag=1.2.3@$runner_digest")
grep -qF "image: ghcr.io/ploeg-hq/ploegd:1.2.3@$runner_digest" <<< "$rendered" || fail 'a tag with its own digest is not used as written'

release="$root/scripts/release.sh"
grep -q 'scripts/package-chart.sh "$version" "$image_digest" "$runner_digest" "$worker_digest" dist' "$release" || fail 'release.sh does not package the chart through scripts/package-chart.sh with all three image digests'
if grep -q '^helm package' "$release"; then fail 'release.sh packages the chart without stamping the image digests'; fi
for read_digest in runner_digest worker_digest; do
  [[ $(grep -n 'scripts/package-chart.sh' "$release" | cut -d: -f1) -gt $(grep -n "^$read_digest=" "$release" | cut -d: -f1) ]] || fail 'release.sh packages the chart before it reads the image digests'
done
echo 'Published chart pins every release image by digest'
