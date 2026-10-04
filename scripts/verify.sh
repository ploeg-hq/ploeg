#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
test -z "$(gofmt -l .)"
go vet ./...
go build ./...
go test -count=1 ./...
bash scripts/brand-marks.sh
bash scripts/license-check.sh
bash scripts/test-release-policy.sh
openspec validate --all --strict
required=ops/helm/ploeg/ci/required-values.yaml
helm lint ops/helm/ploeg -f "$required"
for variant in executor executor-cronjob executor-gitlab monitoring; do
  values="ops/helm/ploeg/ci/${variant}-values.yaml"
  helm lint ops/helm/ploeg -f "$required" -f "$values"
  helm template ploeg ops/helm/ploeg -f "$required" -f "$values" >/dev/null
done
helm template ploeg ops/helm/ploeg -f "$required" >/dev/null
sh scripts/helm-golden.sh check
