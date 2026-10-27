#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
fail() { echo "quickstart test: $*" >&2; exit 1; }
quickstart() { bash "$root/scripts/quickstart.sh" "$@"; }
refuses() {
  local why=$1
  shift
  if "$@" > "$fixture/refused.out" 2>&1; then fail "accepted $why"; fi
}

pinned=$(quickstart pinned)
[[ "$pinned" =~ ^0\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$ ]] || fail "the page pins '$pinned', not a release version"

quickstart script > "$fixture/page.sh"
bash -n "$fixture/page.sh" || fail 'the script generated from the page is not valid bash'
grep -qxF "export PLOEG_VERSION=$pinned" "$fixture/page.sh" || fail 'the generated script lost the pinned version'
grep -qF 'kind delete cluster --name ploeg-quickstart' "$fixture/page.sh" || fail 'the generated script never deletes the cluster'
grep -qx 'trap quickstart_cleanup EXIT' "$fixture/page.sh" || fail 'the generated script does not clean up on failure'
if grep -qE '^(git clone|cd ploeg$)' "$fixture/page.sh"; then fail 'the generated script clones the repository instead of using this checkout'; fi

QUICKSTART_VERSION=0.9.0-rc.1 QUICKSTART_CHART=/charts/ploeg-0.9.0-rc.1.tgz quickstart script > "$fixture/override.sh"
grep -qxF 'export PLOEG_VERSION=0.9.0-rc.1' "$fixture/override.sh" || fail 'QUICKSTART_VERSION did not replace the pin'
grep -qF 'helm install ploeg /charts/ploeg-0.9.0-rc.1.tgz' "$fixture/override.sh" || fail 'QUICKSTART_CHART did not replace the published chart'
if grep -qF 'oci://ghcr.io/ploeg-hq/charts/ploeg' "$fixture/override.sh"; then fail 'QUICKSTART_CHART left the published chart in the install'; fi
if grep -qF 'kind load' "$fixture/override.sh"; then fail 'a run without QUICKSTART_IMAGES loads images into the cluster'; fi

grep -qx '  \[ "$quickstart_status" = 0 \] || quickstart_diagnose' "$fixture/page.sh" || fail 'the generated script does not describe the cluster when a step fails'
grep -qF 'kubectl logs "$pod" --all-containers --prefix --tail=200 --previous' "$fixture/page.sh" || fail 'the diagnostics leave out the logs of a crashed container'
[[ "$(grep -n 'quickstart_diagnose$' "$fixture/page.sh" | cut -d: -f1)" -lt "$(grep -n '^  kind delete cluster' "$fixture/page.sh" | cut -d: -f1)" ]] \
  || fail 'the generated script deletes the cluster before it describes it'

mkdir "$fixture/images" "$fixture/no-images" "$fixture/untagged" "$fixture/layout"
oci_archive() {
  local name=$1 digest=$2 archive=$3
  jq -n --arg name "$name" --arg digest "$digest" \
    '{schemaVersion: 2, manifests: [{digest: $digest, annotations: {"io.containerd.image.name": $name}}]}' > "$fixture/layout/index.json"
  tar -cf "$archive" -C "$fixture/layout" index.json
}
ploegd_digest=sha256:$(printf ploegd | sha256sum | cut -d' ' -f1)
worker_digest=sha256:$(printf ploeg-worker | sha256sum | cut -d' ' -f1)
oci_archive ghcr.io/ploeg-hq/ploegd:checkout "$ploegd_digest" "$fixture/images/ploegd.tar"
oci_archive ghcr.io/ploeg-hq/ploeg-worker:checkout "$worker_digest" "$fixture/images/ploeg-worker.tar"
QUICKSTART_IMAGES="$fixture/images" quickstart script > "$fixture/images.sh"
for image in ploegd ploeg-worker; do
  grep -qxF "kind load image-archive --name ploeg-quickstart $fixture/images/$image.tar" "$fixture/images.sh" \
    || fail "QUICKSTART_IMAGES did not load $image.tar into the cluster"
done
grep -qxF "docker exec ploeg-quickstart-control-plane ctr --namespace k8s.io images tag --force ghcr.io/ploeg-hq/ploegd:checkout ghcr.io/ploeg-hq/ploegd@$ploegd_digest" "$fixture/images.sh" \
  || fail 'the loaded ploegd image is not reachable by the digest the chart pins'
grep -qxF "docker exec ploeg-quickstart-control-plane ctr --namespace k8s.io images tag --force ghcr.io/ploeg-hq/ploeg-worker:checkout ghcr.io/ploeg-hq/ploeg-worker@$worker_digest" "$fixture/images.sh" \
  || fail 'the loaded worker image is not reachable by the digest the chart pins'
oci_archive ghcr.io/ploeg-hq/ploegd:0.2.0 "$ploegd_digest" "$fixture/untagged/ploegd.tar"
refuses 'an image archive that is not a checkout build' env QUICKSTART_IMAGES="$fixture/untagged" bash "$root/scripts/quickstart.sh" script
touch "$fixture/untagged/ploegd.tar"
refuses 'an image archive with no OCI index' env QUICKSTART_IMAGES="$fixture/untagged" bash "$root/scripts/quickstart.sh" script
[[ "$(grep -n '^kind create cluster' "$fixture/images.sh" | cut -d: -f1)" -lt "$(grep -n '^kind load image-archive' "$fixture/images.sh" | head -1 | cut -d: -f1)" ]] \
  || fail 'the generated script loads images before it creates the cluster'
[[ "$(grep -n '^kind load image-archive' "$fixture/images.sh" | tail -1 | cut -d: -f1)" -lt "$(grep -n '^helm install ploeg' "$fixture/images.sh" | cut -d: -f1)" ]] \
  || fail 'the generated script installs the chart before it loads the images'
refuses 'an image directory with no archive' env QUICKSTART_IMAGES="$fixture/no-images" bash "$root/scripts/quickstart.sh" script
sed 's/^kind create cluster --name .*/kind create cluster/' "$root/docs/quickstart.md" > "$fixture/unnamed-cluster.md"
refuses 'images for a cluster the page does not name' env QUICKSTART_IMAGES="$fixture/images" QUICKSTART_DOC="$fixture/unnamed-cluster.md" bash "$root/scripts/quickstart.sh" script

grep -v '^export PLOEG_VERSION=' "$root/docs/quickstart.md" > "$fixture/no-pin.md"
refuses 'a page with no pinned version' env QUICKSTART_DOC="$fixture/no-pin.md" bash "$root/scripts/quickstart.sh" script
grep -v '^kind delete cluster' "$root/docs/quickstart.md" > "$fixture/no-cleanup.md"
refuses 'a page that never deletes its cluster' env QUICKSTART_DOC="$fixture/no-cleanup.md" bash "$root/scripts/quickstart.sh" script
grep -vx 'cd ploeg' "$root/docs/quickstart.md" > "$fixture/no-cd.md"
refuses 'a page whose clone step changed shape' env QUICKSTART_DOC="$fixture/no-cd.md" bash "$root/scripts/quickstart.sh" script
sed 's#oci://ghcr.io/ploeg-hq/charts/ploeg --version "$PLOEG_VERSION"#oci://ghcr.io/ploeg-hq/charts/ploeg#' "$root/docs/quickstart.md" > "$fixture/unversioned.md"
refuses 'a page that installs an unpinned chart' env QUICKSTART_DOC="$fixture/unversioned.md" bash "$root/scripts/quickstart.sh" script

sed "s/^export PLOEG_VERSION=.*/export PLOEG_VERSION=0.2.0-rc.5/" "$root/docs/quickstart.md" > "$fixture/rc5.md"
releases() { for n in $(seq "$1" -1 "$2"); do echo "v0.2.0-rc.$n"; done; }
releases 5 1 > "$fixture/newest.txt"
QUICKSTART_DOC="$fixture/rc5.md" quickstart pin-check < "$fixture/newest.txt" > "$fixture/versions.txt"
grep -qx 'pinned=0.2.0-rc.5' "$fixture/versions.txt" || fail 'pin-check did not report the pinned version'
grep -qx 'newest=0.2.0-rc.5' "$fixture/versions.txt" || fail 'pin-check did not report the newest version'
releases 24 1 > "$fixture/nineteen-behind.txt"
QUICKSTART_DOC="$fixture/rc5.md" quickstart pin-check < "$fixture/nineteen-behind.txt" > /dev/null || fail 'refused a pin 19 releases behind'
releases 25 1 > "$fixture/twenty-behind.txt"
refuses 'a pin 20 releases behind' env QUICKSTART_DOC="$fixture/rc5.md" bash "$root/scripts/quickstart.sh" pin-check < "$fixture/twenty-behind.txt"
grep -qF 'pin 0.2.0-rc.25' "$fixture/refused.out" || fail 'a stale pin does not name the version to pin'
releases 9 6 > "$fixture/unpublished.txt"
refuses 'a pin that is not a published release' env QUICKSTART_DOC="$fixture/rc5.md" bash "$root/scripts/quickstart.sh" pin-check < "$fixture/unpublished.txt"
refuses 'an empty release list' env QUICKSTART_DOC="$fixture/rc5.md" bash "$root/scripts/quickstart.sh" pin-check < /dev/null
echo 'quickstart: the page generates its run script, and pin-check holds the pin to the newest 20 releases'
