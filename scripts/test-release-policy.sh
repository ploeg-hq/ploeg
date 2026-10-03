#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/scripts" "$fixture/ops/helm/ploeg"
cp "$root/scripts/release.sh" "$fixture/scripts/"
cd "$fixture"
git init --quiet -b main
git config user.name 'Release policy test'
git config user.email release-test@example.invalid
printf 'version: 0.1.0\nappVersion: 0.1.0\n' > ops/helm/ploeg/Chart.yaml
git add -- scripts/release.sh ops/helm/ploeg/Chart.yaml
git commit --quiet -m fixture
git update-ref refs/remotes/origin/main HEAD
git tag v0.1.0
bash scripts/release.sh validate v0.1.0
for tag in v1.0.0 v0.01.0 v0.1.0-rc.0 invalid; do
  if bash scripts/release.sh validate "$tag"; then echo "accepted invalid tag $tag" >&2; exit 1; fi
done
printf 'version: 0.1.1\nappVersion: 0.1.1\n' > ops/helm/ploeg/Chart.yaml
if bash scripts/release.sh validate v0.1.0; then echo 'accepted mismatched chart' >&2; exit 1; fi
git add -- ops/helm/ploeg/Chart.yaml
git commit --quiet -m unreviewed
git tag v0.1.1
if bash scripts/release.sh validate v0.1.1; then echo 'accepted off-main tag' >&2; exit 1; fi
echo 'Release tag, version and ancestry checks passed'
