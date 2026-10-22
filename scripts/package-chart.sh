#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
version=${1:?expected the release version}
ploegd_digest=${2:?expected the ploegd image digest}
runner_digest=${3:?expected the claude-runner image digest}
destination=${4:?expected the destination directory}
for digest in "$ploegd_digest" "$runner_digest"; do
  [[ "$digest" =~ ^sha256:[a-f0-9]{64}$ ]] || { echo "$digest is not a sha256 digest; refusing to package a chart without it" >&2; exit 1; }
done
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp -R "$root/ops/helm/ploeg" "$work/ploeg"
if ! awk -v ploegd="\"$ploegd_digest\"" -v runner="\"$runner_digest\"" '
  /^[^ #]/ { section = $1; sub(/:.*/, "", section); runner_image = 0 }
  section == "claudeRunner" && /^  [^ #]/ { runner_image = ($1 == "image:") }
  section == "image" && /^  digest: ""$/ { sub(/""/, ploegd); stamped++ }
  section == "claudeRunner" && runner_image && /^    digest: ""$/ { sub(/""/, runner); stamped++ }
  { print }
  END { if (stamped != 2) exit 1 }
' "$root/ops/helm/ploeg/values.yaml" > "$work/ploeg/values.yaml"; then
  echo "ops/helm/ploeg/values.yaml must hold exactly the empty image.digest and claudeRunner.image.digest the release stamps" >&2
  exit 1
fi
mkdir -p "$destination"
helm package "$work/ploeg" --version "$version" --app-version "$version" --destination "$destination"
