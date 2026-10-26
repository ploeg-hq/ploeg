#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
bash scripts/check-provenance.sh
test -z "$(gofmt -l .)"
go vet ./...
go build ./...
go test -count=1 ./...
bash scripts/brand-marks.sh
bash scripts/license-check.sh
bash scripts/test-release-policy.sh
bash scripts/test-package-chart.sh
bash scripts/test-contract-bundle.sh
bash scripts/test-go-module-published.sh
bash scripts/test-release-consumers.sh
bash scripts/test-dco-check.sh
bash scripts/test-claude-code-install.sh
bash scripts/test-check-go-toolchain.sh
openspec validate --all --strict
required=ops/helm/ploeg/ci/required-values.yaml
uv run --no-project --with pyyaml==6.0.3 --with cel-python==0.4.0 python scripts/check-security-profile.py
helm lint ops/helm/ploeg -f "$required"
for variant in executor executor-cronjob executor-gitlab executor-github monitoring knowledge-graph mcp claude-runner name-overrides; do
  values="ops/helm/ploeg/ci/${variant}-values.yaml"
  helm lint ops/helm/ploeg -f "$required" -f "$values"
  helm template ploeg ops/helm/ploeg -f "$required" -f "$values" >/dev/null
done
helm template ploeg ops/helm/ploeg -f "$required" >/dev/null
helm lint ops/helm/ploeg -f ops/quickstart/values.yaml
helm template ploeg ops/helm/ploeg -n ploeg -f ops/quickstart/values.yaml >/dev/null
sh scripts/helm-golden.sh check
bash scripts/test-chart-network-policies.sh
bash scripts/test-worker-boundary-probe.sh
bash scripts/test-alert-rules.sh
