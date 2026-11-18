#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
chart="$root/ops/helm/ploeg"
work=$(mktemp -d)
server=""
cleanup() {
  [ -z "$server" ] || kill "$server" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT
fail() { echo "claude-runner orchestrator: $*" >&2; exit 1; }

helm template ploeg "$chart" -f "$chart/ci/required-values.yaml" -f "$chart/ci/executor-credential-gateway-claude-environment-values.yaml" >"$work/render.yaml"
check() {
  uv run -q --no-project --with pyyaml==6.0.3 python "$root/scripts/check-claude-runner-orchestrator.py" "$1" ploeg-workers
}
mutate() {
  uv run -q --no-project --with pyyaml==6.0.3 python - "$work/render.yaml" "$2" >"$1" <<'PY'
import json
import sys
import yaml

documents = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
config = next(d for d in documents if d.get('kind') == 'ConfigMap' and d['metadata']['name'].endswith('-orchestrator'))
job = json.loads(config['data']['job.json'])
pod = job['spec']['template']['spec']
runner = next(c for c in pod['containers'] if c['name'] == 'runner')
orchestrator = next(d for d in documents if d.get('kind') == 'Deployment' and d['metadata']['name'].endswith('-orchestrator'))['spec']['template']['spec']
session_policy = next(d for d in documents if d.get('kind') == 'NetworkPolicy' and d['metadata']['name'].endswith('-claude-runner-session'))
exec(sys.argv[2])
config['data']['job.json'] = json.dumps(job)
yaml.safe_dump_all(documents, sys.stdout)
PY
}

check "$work/render.yaml" >/dev/null || fail 'the check refuses the render it must pass'
cases=(
  "runner['env'].append({'name': 'SELF_HOSTED_RUNNER_ENVIRONMENT_SECRET', 'valueFrom': {'secretKeyRef': {'name': 'claude-runner-environment', 'key': 'SELF_HOSTED_RUNNER_ENVIRONMENT_SECRET'}}})|holds the environment secret"
  "runner['env'].append({'name': 'CLAUDE_CODE_OAUTH_TOKEN', 'value': 'x'})|renders CLAUDE_CODE_OAUTH_TOKEN"
  "runner.setdefault('envFrom', []).append({'secretRef': {'name': 'anything'}})|takes envFrom"
  "pod['volumes'].append({'name': 'login', 'persistentVolumeClaim': {'claimName': 'pewter-claude-login'}})|volume login is of type persistentVolumeClaim"
  "pod['volumes'].append({'name': 'token', 'projected': {'sources': []}})|volume token is of type projected"
  "pod['automountServiceAccountToken'] = True|automountServiceAccountToken is not false"
  "runner['env'].append({'name': 'PLOEG_CLAUDE_RUNNER_WORK_ORDER', 'value': 'x'})|carries a work order"
  "orchestrator['affinity']['podAntiAffinity']['requiredDuringSchedulingIgnoredDuringExecution'].pop()|carries no required anti-affinity"
  "orchestrator['containers'][0]['args'] += ['--min-idle', '1']|--min-idle"
  "session_policy['spec']['egress'].append({'to': [{'podSelector': {}}], 'ports': [{'port': 8080, 'protocol': 'TCP'}]})|reach more than DNS, the gateway and Anthropic"
)
for case in "${cases[@]}"; do
  code=${case%%|*}
  want=${case##*|}
  mutate "$work/mutated.yaml" "$code"
  if out=$(check "$work/mutated.yaml"); then
    fail "the check passed a render where $code"
  fi
  printf '%s' "$out" | grep -qF -- "$want" || fail "the check refused $code without naming '$want': $out"
done

mkdir -p "$work/sa" "$work/api"
printf 'service-account-token' >"$work/sa/token"
printf 'eyJ.work-order.sig\n' >"$work/order"
uv run -q --no-project --with pyyaml==6.0.3 python - "$work/render.yaml" >"$work/job.json" <<'PY'
import sys
import yaml
documents = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
print(next(d for d in documents if d.get('kind') == 'ConfigMap' and d['metadata']['name'].endswith('-orchestrator'))['data']['job.json'])
PY
port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')
python3 - "$port" "$work/api" <<'PY' &
import http.server
import json
import os
import sys

seen = set()
out = sys.argv[2]

class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers['Content-Length']))
        job = json.loads(body)
        name = job['metadata']['name']
        with open(os.path.join(out, 'requests'), 'a') as f:
            f.write(json.dumps({'path': self.path, 'auth': self.headers.get('Authorization'), 'job': job}) + '\n')
        status = 409 if name in seen else (422 if name.endswith('-refused') else 201)
        seen.add(name)
        self.send_response(status)
        self.end_headers()
        self.wfile.write(b'{}')

    def do_GET(self):
        self.send_response(200)
        self.end_headers()

    def log_message(self, *args):
        pass

http.server.HTTPServer(('127.0.0.1', int(sys.argv[1])), Handler).serve_forever()
PY
server=$!
disown "$server" 2>/dev/null || true
for _ in $(seq 50); do
  curl -fsS -o /dev/null "http://127.0.0.1:$port/" 2>/dev/null && break
  sleep 0.1
done
spawn() {
  env -i PATH="$PATH" POD_NAMESPACE=ploeg-workers \
    PLOEG_CLAUDE_RUNNER_JOB="$work/job.json" PLOEG_CLAUDE_RUNNER_JOB_PREFIX=ploeg-claude-runner \
    PLOEG_KUBERNETES_SERVICEACCOUNT_DIR="$work/sa" PLOEG_KUBERNETES_API_URL="http://127.0.0.1:$port" \
    CLAUDE_RUNNER_WORK_ORDER_FILE="$work/order" "$@" "$root/ops/claude-runner/spawn-runner"
}
spawn CLAUDE_RUNNER_ORDER_ID=order-1 CLAUDE_RUNNER_SESSION_ID=session_abc 2>/dev/null || fail 'the hook did not submit the runner Job'
spawn CLAUDE_RUNNER_ORDER_ID=order-1 CLAUDE_RUNNER_SESSION_ID=session_abc 2>/dev/null || fail 'a redelivered spawn request failed instead of finding its Job'
status=0
spawn CLAUDE_RUNNER_ORDER_ID=order-2 2>/dev/null || status=$?
[ "$status" -eq 2 ] || fail "a pre-warming request without a session exited $status, want 2"
status=0
spawn CLAUDE_RUNNER_ORDER_ID=order-3 CLAUDE_RUNNER_SESSION_ID=session_def PLOEG_KUBERNETES_API_URL=http://127.0.0.1:1 2>/dev/null || status=$?
[ "$status" -eq 1 ] || fail "an unreachable Kubernetes API exited $status, want 1 so the request is retried"
uv run -q --no-project python - "$work/api/requests" <<'PY' || fail 'the submitted runner Job is not the template with only its name, session and work order set'
import json
import sys
requests = [json.loads(line) for line in open(sys.argv[1])]
assert len(requests) == 2, len(requests)
first, second = requests
assert first['path'] == '/apis/batch/v1/namespaces/ploeg-workers/jobs', first['path']
assert first['auth'] == 'Bearer service-account-token'
job = first['job']
assert job['metadata']['name'] == second['job']['metadata']['name']
assert job['metadata']['name'].startswith('ploeg-claude-runner-') and len(job['metadata']['name']) <= 63
assert job['spec']['template']['metadata']['annotations']['ploeg.webgrip.dev/claude-session'] == 'session_abc'
runner = next(c for c in job['spec']['template']['spec']['containers'] if c['name'] == 'runner')
orders = [e for e in runner['env'] if e['name'] == 'PLOEG_CLAUDE_RUNNER_WORK_ORDER']
assert orders == [{'name': 'PLOEG_CLAUDE_RUNNER_WORK_ORDER', 'value': 'eyJ.work-order.sig'}], orders
assert not any('secretKeyRef' in (e.get('valueFrom') or {}) for e in runner['env'])
assert job['spec']['template']['spec']['automountServiceAccountToken'] is False
PY
mkdir -p "$work/runner/bin" "$work/runner/order" "$work/runner/site"
: >"$work/runner/site/healthz"
runner_port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')
cat >"$work/runner/bin/claude" <<FAKE
#!/usr/bin/env bash
order="\$3"
{ [ -s "\$order" ] && echo "read at start"; } || echo "missing at start"
env | grep -q '^PLOEG_CLAUDE_RUNNER_WORK_ORDER=' && echo "work order in the runner's environment"
python3 -m http.server --bind 127.0.0.1 $runner_port --directory "$work/runner/site" >/dev/null 2>&1 &
server=\$!
for _ in \$(seq 50); do [ -e "\$order" ] || break; sleep 0.2; done
[ -e "\$order" ] && echo "kept after registration" || echo "deleted after registration"
kill "\$server"
FAKE
chmod +x "$work/runner/bin/claude"
runner_out=$(env -i PATH="$work/runner/bin:$PATH" PLOEG_CLAUDE_RUNNER_WORK_ORDER=eyJ.work-order.sig PLOEG_CLAUDE_RUNNER_WORK_ORDER_DIR="$work/runner/order" \
  PLOEG_CLAUDE_RUNNER_HEALTH_URL="http://127.0.0.1:$runner_port/healthz" "$root/ops/claude-runner/runner-from-work-order" --capacity 1)
[ "$runner_out" = "$(printf 'read at start\ndeleted after registration')" ] || fail "the runner's work order: $runner_out"
echo 'claude-runner orchestrator: ok'
