#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
chart="$root/ops/helm/ploeg"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
keys=silver--builder,silver--reviewer,zinc--default,tin--default,copper--default
helm template ploeg "$chart" -f "$chart/ci/required-values.yaml" -f "$chart/ci/executor-credential-gateway-values.yaml" >"$work/render.yaml"

check() {
  uv run -q --no-project --with pyyaml==6.0.3 python "$root/scripts/check-credential-gateway.py" "$1" ploeg-workers "$keys"
}

mutate() {
  uv run -q --no-project --with pyyaml==6.0.3 python - "$work/render.yaml" "$2" >"$1" <<'PY'
import sys
import yaml

documents = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
job = next(d for d in documents if d.get('kind') == 'ScaledJob' and d['metadata']['name'] == 'ploeg-worker-silver-builder')
pod = job['spec']['jobTargetRef']['template']['spec']
worker = next(c for c in pod['containers'] if c['name'] == 'worker')
policy = next(d for d in documents if d.get('kind') == 'NetworkPolicy' and d['metadata']['name'].endswith('-worker'))
adapter = next(d for d in documents if d.get('kind') == 'Deployment' and d['metadata']['name'].endswith('-mcp-grafana'))['spec']['template']
exec(sys.argv[2])
yaml.safe_dump_all(documents, sys.stdout)
PY
}

check "$work/render.yaml" >/dev/null || { echo 'the credential gateway check refuses the gateway render it must pass' >&2; exit 1; }
helm template ploeg "$chart" -f "$chart/ci/required-values.yaml" -f "$chart/ci/executor-credential-gateway-values.yaml" \
  --set-json 'executor.harness.telemetry={"endpoint":"http://otel-collector.observability.svc:4318","allowHarnesses":["openhands"]}' \
  --set-json 'networkPolicy.worker.extraEgress=[{"to":[{"ipBlock":{"cidr":"203.0.113.9/32"}}],"ports":[{"protocol":"TCP","port":443}]}]' >"$work/extra.yaml"
if ! out=$(check "$work/extra.yaml"); then
  echo 'a telemetry endpoint or networkPolicy.worker.extraEgress widens the egress of a gateway-mode Run pod:' >&2
  echo "$out" >&2
  exit 1
fi

cases=(
  "worker.setdefault('securityContext', {})['privileged'] = True|container worker is privileged"
  "pod['hostNetwork'] = True|the pod sets hostNetwork"
  "worker['env'].append({'name': 'DOCKER_HOST', 'value': 'tcp://localhost:2376'})|container worker renders DOCKER_HOST"
  "worker['env'].append({'name': 'DOCKER_CERT_PATH', 'value': '/certs/client'})|container worker renders DOCKER_CERT_PATH"
  "pod['volumes'].append({'name': 'docker-certs', 'emptyDir': {}}); worker['volumeMounts'].append({'name': 'docker-certs', 'mountPath': '/certs'})|a Docker daemon's client certificates"
  "pod['volumes'].append({'name': 'injected', 'hostPath': {'path': '/var/lib/kubelet'}})|volume injected is of type hostPath"
  "pod['volumes'].append({'name': 'injected', 'csi': {'driver': 'secrets-store.csi.k8s.io'}})|volume injected is of type csi"
  "pod['volumes'].append({'name': 'injected', 'persistentVolumeClaim': {'claimName': 'login'}})|volume injected is of type persistentVolumeClaim"
  "next(d for d in documents if d.get('kind') == 'SandboxTemplate')['spec']['networkPolicyManagement'] = 'Managed'|adds a managed NetworkPolicy"
  "del adapter['metadata']['labels']['ploeg.webgrip.dev/team']|names no Team"
  "adapter['spec']['containers'][0]['volumeMounts'].append({'name': 'sessions', 'mountPath': '/tmp'})|mounts a /tmp its sessions share"
  "adapter['spec']['containers'][0]['command'].remove('--sessions-dir=/sessions')|gives its sessions no directories of their own"
  "policy['spec']['egress'].append({'to': [{'ipBlock': {'cidr': '0.0.0.0/0'}}], 'ports': [{'protocol': 'TCP', 'port': 4318}]})|lets Run pods reach more than DNS and the gateway"
)
for case in "${cases[@]}"; do
  mutation=${case%%|*}
  expected=${case#*|}
  mutate "$work/mutated.yaml" "$mutation"
  if out=$(check "$work/mutated.yaml"); then
    echo "the credential gateway check passed a Run pod after: $mutation" >&2
    exit 1
  fi
  if ! grep -qF -- "$expected" <<<"$out"; then
    echo "the credential gateway check refused '$mutation' for the wrong reason (want '$expected'):" >&2
    echo "$out" >&2
    exit 1
  fi
done
