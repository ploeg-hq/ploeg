#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
probe="$root/scripts/worker-boundary-probe.sh"
work=$(mktemp -d)
listener_pid=""
cleanup() {
  [ -n "$listener_pid" ] && kill "$listener_pid" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

port=$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')
python3 -m http.server --bind 127.0.0.1 --directory "$work" "$port" >/dev/null 2>&1 &
listener_pid=$!
for _ in $(seq 50); do
  curl -s -o /dev/null "http://127.0.0.1:$port/" && break
  sleep 0.1
done
listening="http://127.0.0.1:$port/"
black_hole="http://192.0.2.1/"

confined_root() {
  local dir="$work/$1"
  mkdir -p "$dir/proc/self"
  cat >"$dir/proc/self/status" <<'STATUS'
Name:	sh
Uid:	1000	1000	1000	1000
Gid:	1000	1000	1000	1000
NoNewPrivs:	1
Seccomp:	2
CapInh:	0000000000000000
CapPrm:	0000000000000000
CapEff:	0000000000000000
CapBnd:	0000000000000000
CapAmb:	0000000000000000
STATUS
  cat >"$dir/proc/self/mountinfo" <<'MOUNTS'
1 0 0:1 / / rw - overlay overlay rw
2 1 0:2 / /work rw - tmpfs tmpfs rw
MOUNTS
  printf '%s' "$dir"
}

run_probe() {
  local fixture=$1
  shift
  env -u KUBERNETES_SERVICE_HOST -u KUBERNETES_SERVICE_PORT -u DOCKER_HOST \
    PROBE_ROOT="$fixture" \
    PROBE_CONNECT_TIMEOUT=1 \
    PROBE_METADATA_URLS="$black_hole" \
    PROBE_KUBERNETES_API_URLS="$black_hole" \
    PROBE_BLOCKED_URLS="$black_hole" \
    PROBE_REACHABLE_URLS="$listening" \
    "$@" sh "$probe"
}

expect_pass() {
  local output
  if ! output=$(run_probe "$@"); then
    printf '%s\n' "$output" >&2
    echo "worker-boundary-probe refused a confined pod" >&2
    exit 1
  fi
  grep -q '^PASS allowed reachable' <<<"$output" || { echo "the confined case never reached its allowed endpoint" >&2; exit 1; }
}

expect_fail() {
  local name=$1 expected=$2 output
  shift 2
  if output=$(run_probe "$@"); then
    printf '%s\n' "$output" >&2
    echo "worker-boundary-probe passed a pod with $name" >&2
    exit 1
  fi
  grep -q "^FAIL $expected" <<<"$output" || { printf '%s\n' "$output" >&2; echo "$name failed without naming '$expected'" >&2; exit 1; }
}

status_with() {
  local fixture
  fixture=$(confined_root "$1")
  sed -i "s/^$2:.*/$2:\t$3/" "$fixture/proc/self/status"
  printf '%s' "$fixture"
}

expect_pass "$(confined_root confined)" env

expect_fail "uid 0" "privilege runs as uid 0" "$(status_with root Uid '0	0	0	0')" env
expect_fail "an effective capability" "privilege CapEff=" "$(status_with cap-eff CapEff 0000000000200000)" env
expect_fail "a bounding capability" "privilege CapBnd=" "$(status_with cap-bnd CapBnd 00000000a80425fb)" env
expect_fail "privilege escalation allowed" "privilege no_new_privs not set" "$(status_with escalation NoNewPrivs 0)" env
expect_fail "seccomp off" "privilege seccomp mode 0" "$(status_with unconfined Seccomp 0)" env

fixture=$(confined_root token)
mkdir -p "$fixture/var/run/secrets/kubernetes.io/serviceaccount"
touch "$fixture/var/run/secrets/kubernetes.io/serviceaccount/token"
expect_fail "a service-account token" "kubernetes-token present" "$fixture" env

fixture=$(confined_root docker-socket)
mkdir -p "$fixture/var/run"
touch "$fixture/var/run/docker.sock"
expect_fail "the Docker socket" "host-mount /var/run/docker.sock exists" "$fixture" env

fixture=$(confined_root kubelet-mount)
echo '3 1 8:1 /var/lib/kubelet /var/lib/kubelet rw - ext4 /dev/sda1 rw' >>"$fixture/proc/self/mountinfo"
expect_fail "the kubelet directory mounted" "host-mount /var/lib/kubelet is a mount point" "$fixture" env

expect_fail "DOCKER_HOST" "host-mount DOCKER_HOST is set" "$(confined_root docker-host)" env DOCKER_HOST=tcp://localhost:2376
expect_fail "a reachable metadata endpoint" "metadata reachable" "$(confined_root metadata)" env PROBE_METADATA_URLS="$listening"
expect_fail "a reachable Kubernetes API" "kubernetes-api reachable" "$(confined_root api)" env PROBE_KUBERNETES_API_URLS="$listening"
expect_fail "a reachable Kubernetes API Service" "kubernetes-api reachable https://127.0.0.1:$port/version" "$(confined_root api-service)" env KUBERNETES_SERVICE_HOST=127.0.0.1 KUBERNETES_SERVICE_PORT="$port"
expect_fail "unapproved egress" "egress reachable" "$(confined_root egress)" env PROBE_BLOCKED_URLS="$listening"
expect_fail "an unreachable allowed endpoint" "allowed unreachable" "$(confined_root allowed)" env PROBE_REACHABLE_URLS="$black_hole"

echo "worker-boundary-probe: 1 confined case passed, 14 escapes refused"
