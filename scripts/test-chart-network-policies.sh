#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
chart="$root/ops/helm/ploeg"
ci="$chart/ci"
required="$ci/required-values.yaml"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
fail() { echo "$1" >&2; exit 1; }

render() { helm template ploeg "$chart" -f "$required" "$@" -s templates/networkpolicy.yaml 2>/dev/null || true; }
policy() { # <rendered> <name>: the one NetworkPolicy document with that name
  awk -v needle="
  name: $2
" 'BEGIN { RS = "---\n" } index($0, needle) { print }' <<< "$1"
}
count_policies() { grep -c '^kind: NetworkPolicy$' <<< "$1" || true; }
assert_has() { grep -qF -- "$2" <<< "$1" || fail "$3"; }
assert_lacks() { if grep -qF -- "$2" <<< "$1"; then fail "$3"; fi; }
assert_count() { local got; got=$(grep -cF -- "$2" <<< "$1" || true); [[ "$got" == "$3" ]] || fail "$4 (found $got)"; }
verdict() {
  local file="$work/verdict.yaml" code=0
  printf '%s\n' "$1" > "$file"
  uv run -q --no-project --with pyyaml==6.0.3 python "$root/scripts/netpol-admits.py" "$file" "$2" "$3" "$4" >/dev/null 2>&1 || code=$?
  echo "$code"
}
assert_admits() { [[ "$(verdict "$1" "$2" "$3" "$4")" == 0 ]] || fail "$5"; }
assert_denies() { [[ "$(verdict "$1" "$2" "$3" "$4")" == 1 ]] || fail "$5"; }

rendered=$(render)
[[ "$(count_policies "$rendered")" == 1 ]] || fail 'a default install renders one policy, for ploegd'
ploegd=$(policy "$rendered" ploeg-ploegd)
assert_has "$ploegd" 'ploeg.webgrip.dev/component: daemon' 'the ploegd policy does not select the daemon pod'
assert_has "$ploegd" 'policyTypes: [Ingress, Egress]' 'the ploegd policy is not default-deny in both directions'
assert_has "$ploegd" 'k8s-app: kube-dns' 'ploegd cannot resolve names'
assert_has "$ploegd" 'port: 5432' 'ploegd cannot reach its database'
assert_has "$ploegd" 'kubernetes.io/metadata.name: ai' 'ploegd cannot reach the gateway namespace its adminUrl names'
assert_has "$ploegd" 'port: 4000' 'the gateway rule does not carry the port of executor.litellm.adminUrl'
assert_has "$ploegd" 'kubernetes.io/metadata.name: forgejo' 'ploegd cannot reach the forge namespace executor.forgejo.url names'
assert_has "$ploegd" 'port: 8080' 'ploegd accepts nothing on its listen port'
assert_lacks "$ploegd" 'cidr: 0.0.0.0/0' 'a default install with in-cluster URLs lets ploegd reach the internet'

rendered=$(render --set networkPolicy.enabled=false --set supportProfile=unsupported)
[[ "$(count_policies "$rendered")" == 0 ]] || fail 'networkPolicy.enabled=false still renders a policy'

rendered=$(render -f "$ci/executor-values.yaml")
worker=$(policy "$rendered" ploeg-worker)
[[ -n "$worker" ]] || fail 'the executor case renders no worker policy'
assert_has "$worker" 'app.kubernetes.io/name: ploeg-worker' 'the worker policy does not select worker pods'
assert_has "$worker" 'ingress: []' 'worker pods accept ingress'
assert_has "$worker" 'ploeg.webgrip.dev/component: daemon' 'workers cannot reach ploegd'
assert_has "$worker" 'kubernetes.io/metadata.name: ai' 'workers cannot reach the gateway'
assert_has "$worker" 'kubernetes.io/metadata.name: forgejo' 'workers cannot reach the forge'
assert_has "$worker" 'kubernetes.io/metadata.name: observability' 'workers cannot reach the OTLP collector a team names'
assert_has "$worker" 'port: 4318' 'the telemetry rule does not carry the collector port'
assert_lacks "$worker" 'cidr: 0.0.0.0/0' 'an airgapped worker pool reaches the internet'
assert_lacks "$worker" 'port: 5432' 'workers reach the database'
pewter=$(policy "$rendered" ploeg-worker-pewter-anthropic)
assert_has "$pewter" 'ploeg.webgrip.dev/team: pewter' 'the claude-environment team policy does not select its team'
assert_has "$pewter" 'cidr: 0.0.0.0/0' 'a claude-environment team cannot reach Anthropic'
assert_has "$pewter" '172.16.0.0/12' 'the internet rule does not exclude private ranges'
[[ -z "$(policy "$rendered" ploeg-worker-brass-anthropic)" ]] || fail 'a client-carried claude-environment team gets an Anthropic policy it has no worker for'
[[ -z "$(policy "$rendered" ploeg-worker-bronze-anthropic)" ]] || fail 'a team without claude-environment gets an Anthropic policy'
ploegd=$(policy "$rendered" ploeg-ploegd)
assert_has "$ploegd" 'cidr: 0.0.0.0/0' 'ploegd cannot reach the public forge URL or the Anthropic JWKS'

rendered=$(render -f "$ci/executor-sandbox-values.yaml")
launcher=$(policy "$rendered" ploeg-sandbox-launcher)
[[ -n "$launcher" ]] || fail 'the sandbox case renders no launcher policy'
assert_has "$launcher" 'app.kubernetes.io/name: ploeg-sandbox-launcher' 'the launcher policy does not select launcher pods'
assert_has "$launcher" 'ingress: []' 'launcher pods accept ingress'
assert_has "$launcher" 'port: 6443' 'the launcher cannot reach the Kubernetes API'
assert_has "$launcher" 'ploeg.webgrip.dev/component: daemon' 'the launcher cannot reach ploegd'
assert_lacks "$launcher" 'port: 4000' 'the launcher reaches the model gateway'
[[ -n "$(policy "$rendered" ploeg-worker)" ]] || fail 'sandbox worker pods have no policy'

rendered=$(render -f "$ci/claude-runner-values.yaml")
runner=$(policy "$rendered" ploeg-claude-runner)
[[ -n "$runner" ]] || fail 'the claude-runner case renders no runner policy'
assert_has "$runner" 'app.kubernetes.io/name: claude-runner' 'the runner policy does not select the runner pods by their name'
assert_has "$runner" 'ingress: []' 'runner pods accept ingress'
assert_has "$runner" 'cidr: 0.0.0.0/0' 'the runner cannot reach Anthropic'
assert_has "$runner" 'port: 4318' 'the runner cannot reach the OTLP endpoint its env names'
assert_has "$runner" 'kubernetes.io/metadata.name: forgejo' 'the runner cannot reach the forge'

rendered=$(render -f "$ci/mcp-values.yaml")
mcp=$(policy "$rendered" ploeg-mcp)
[[ -n "$mcp" ]] || fail 'the mcp case renders no ploeg-mcp policy'
assert_has "$mcp" 'port: 8090' 'ploeg-mcp accepts nothing on mcp.port'
assert_has "$mcp" 'ploeg.webgrip.dev/component: daemon' 'ploeg-mcp cannot reach ploegd'
assert_has "$mcp" 'cidr: 0.0.0.0/0' 'ploeg-mcp cannot reach the identity provider PLOEG_MCP_ISSUER names'

rendered=$(render -f "$ci/executor-github-values.yaml")
ploegd=$(policy "$rendered" ploeg-ploegd)
assert_count "$ploegd" 'cidr: 0.0.0.0/0' 1 'github.com and api.github.com render as two internet rules instead of one'
worker=$(policy "$rendered" ploeg-worker)
assert_has "$worker" 'cidr: 0.0.0.0/0' 'workers on a GitHub forge cannot reach it'

cat > "$work/named.yaml" <<'EOF'
networkPolicy:
  gateway:
    peers:
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: inference
        podSelector:
          matchLabels:
            app: litellm
    port: 4001
  database:
    peers:
      - podSelector:
          matchLabels:
            cnpg.io/cluster: ploeg-db
  ploegd:
    ingressFrom:
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: gateway
    extraEgress:
      - to:
          - ipBlock:
              cidr: 203.0.113.7/32
        ports:
          - {protocol: TCP, port: 9999}
  forge:
    peers:
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: forgejo
        podSelector:
          matchLabels:
            app.kubernetes.io/name: forgejo
  worker:
    extraEgress:
      - to:
          - ipBlock:
              cidr: 203.0.113.8/32
        ports:
          - {protocol: TCP, port: 3128}
EOF
rendered=$(render -f "$ci/executor-values.yaml" -f "$work/named.yaml")
ploegd=$(policy "$rendered" ploeg-ploegd)
assert_has "$ploegd" 'app: litellm' 'named gateway peers do not replace the derived gateway rule for ploegd'
assert_has "$ploegd" 'port: 4001' 'a named gateway port does not replace the URL port'
assert_lacks "$ploegd" 'kubernetes.io/metadata.name: ai' 'the derived gateway rule survives named peers'
assert_has "$ploegd" 'cnpg.io/cluster: ploeg-db' 'named database peers do not reach the ploegd policy'
assert_lacks "$ploegd" 'podSelector: {}' 'the namespace-wide database rule survives named peers'
assert_has "$ploegd" 'kubernetes.io/metadata.name: gateway' 'ploegd.ingressFrom is not rendered'
assert_has "$ploegd" 'ploeg-sandbox-launcher' 'naming ingress peers cuts off this release'"'"'s own pods'
assert_has "$ploegd" 'cidr: 203.0.113.7/32' 'ploegd.extraEgress is not rendered'
worker=$(policy "$rendered" ploeg-worker)
assert_has "$worker" 'app: litellm' 'named gateway peers do not reach the worker policy'
assert_has "$worker" 'cidr: 203.0.113.8/32' 'worker.extraEgress is not rendered'
assert_has "$worker" 'app.kubernetes.io/name: forgejo' 'named forge peers for a svc URL do not reach the worker policy'
assert_count "$worker" 'kubernetes.io/metadata.name: forgejo' 1 'the namespace-wide forge rule survives named peers for a svc URL'
assert_lacks "$worker" 'cidr: 203.0.113.7/32' 'ploegd.extraEgress leaks into the worker policy'

rendered=$(render -f "$ci/executor-values.yaml" --set 'networkPolicy.privateCidrs=null')
assert_denies "$rendered" ploeg-worker-pewter-anthropic 169.254.169.254 443 'an empty privateCidrs lets a claude-environment worker reach link-local addresses'
assert_denies "$rendered" ploeg-worker-pewter-anthropic 127.0.0.1 443 'an empty privateCidrs lets a claude-environment worker reach loopback'
assert_admits "$rendered" ploeg-worker-pewter-anthropic 10.1.2.3 443 'an empty privateCidrs still excepts a private range'
assert_admits "$rendered" ploeg-worker-pewter-anthropic 1.1.1.1 443 'a claude-environment worker cannot reach a public address on 443'

rendered=$(render -f "$ci/executor-values.yaml" --set 'networkPolicy.privateCidrs={10.0.0.0/8,169.254.0.0/16}')
pewter=$(policy "$rendered" ploeg-worker-pewter-anthropic)
assert_count "$pewter" '169.254.0.0/16' 1 'a privateCidrs entry that is also node-local is excepted twice'
assert_count "$pewter" '127.0.0.0/8' 1 'loopback is not excepted once when privateCidrs is set'

rendered=$(render -f "$ci/executor-public-endpoints-values.yaml")
assert_denies "$rendered" ploeg-worker 169.254.169.254 80 'workers on a public http forge reach the instance metadata service on 80'
assert_denies "$rendered" ploeg-worker 169.254.169.254 443 'workers on a public https gateway reach the instance metadata service on 443'
assert_denies "$rendered" ploeg-worker 127.0.0.1 80 'workers on a public http forge reach loopback on 80'
assert_denies "$rendered" ploeg-worker 100.64.0.1 80 'workers on a public http forge reach the shared address space on 80'
assert_denies "$rendered" ploeg-worker 10.43.0.1 80 'workers on a public http forge reach a private range on 80'
assert_admits "$rendered" ploeg-worker 93.184.215.14 80 'workers cannot reach a public http forge on 80'
assert_admits "$rendered" ploeg-worker 93.184.215.14 443 'workers cannot reach a public https gateway on 443'
assert_denies "$rendered" ploeg-worker 93.184.215.14 22 'the public endpoint rules open a port no URL names'
assert_denies "$rendered" ploeg-ploegd 169.254.169.254 80 'ploegd on a public http forge reaches the instance metadata service on 80'

echo 'Every Ploeg pod the chart renders is under a default-deny NetworkPolicy with its own allowlist'
