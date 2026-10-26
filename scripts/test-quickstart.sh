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
