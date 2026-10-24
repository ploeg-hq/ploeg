#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
version=${1:?expected the release version}
ploegd_digest=${2:?expected the ploegd image digest}
runner_digest=${3:?expected the claude-runner image digest}
worker_digest=${4:?expected the ploeg-worker image digest}
destination=${5:?expected the destination directory}
for digest in "$ploegd_digest" "$runner_digest" "$worker_digest"; do
  [[ "$digest" =~ ^sha256:[a-f0-9]{64}$ ]] || { echo "$digest is not a sha256 digest; refusing to package a chart without it" >&2; exit 1; }
done
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp -R "$root/ops/helm/ploeg" "$work/ploeg"
cp "$root/docs/ops/self-hosting-security.md" "$work/ploeg/SECURITY.md"
if ! awk -v ploegd="\"$ploegd_digest\"" -v runner="\"$runner_digest\"" -v worker="\"$worker_digest\"" '
  /^[^ #]/ { section = $1; sub(/:.*/, "", section); nested_image = 0 }
  (section == "claudeRunner" || section == "worker") && /^  [^ #]/ { nested_image = ($1 == "image:") }
  section == "image" && /^  digest: ""$/ { sub(/""/, ploegd); stamped++ }
  section == "claudeRunner" && nested_image && /^    digest: ""$/ { sub(/""/, runner); stamped++ }
  section == "worker" && nested_image && /^    digest: ""$/ { sub(/""/, worker); stamped++ }
  { print }
  END { if (stamped != 3) exit 1 }
' "$root/ops/helm/ploeg/values.yaml" > "$work/ploeg/values.yaml"; then
  echo "ops/helm/ploeg/values.yaml must hold exactly the empty image.digest, claudeRunner.image.digest and worker.image.digest the release stamps" >&2
  exit 1
fi
mkdir -p "$destination"
helm package "$work/ploeg" --version "$version" --app-version "$version" --destination "$destination"
