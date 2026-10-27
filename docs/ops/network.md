---
type: reference
audience: [operator, integrator, contributor]
owner: ploeg
last_verified: 2026-10-28
verified_by: "Rendered ops/helm/ploeg with every ci/*-values.yaml and read the output; scripts/test-chart-network-policies.sh asserts each row below on every verify run; source read for the callers: cmd/ploegd/boot.go, pkg/worker, pkg/sandboxlaunch/launcher.go, pkg/claudesession/verifier.go, ops/claude-runner"
---

# Network policies

The chart renders a `networking.k8s.io/v1` NetworkPolicy for every pod it creates. Each policy selects one kind of pod, lists both `Ingress` and `Egress` in `policyTypes`, and allows only the rows below. A pod the policies select can reach nothing else, and nothing else can reach it. This is the chart's answer to threat-model finding [F-02](../security/threat-model.md#findings) ([ADR-0137](../adrs/0137-the-chart-renders-a-default-deny-networkpolicy-for-every-pod-with-an-allowlist-derived-from-its-urls.md)).

`networkPolicy.enabled: true` is the default. `false` renders none.

## What a policy is worth on your cluster

A NetworkPolicy is a request to the CNI plugin, and only a plugin that implements the API enforces it. Cilium, Calico and Antrea do. kind's default `kindnet` and Flannel accept the objects and ignore them, so on the [quickstart](../quickstart.md) cluster every pod can still reach everything. `kubectl get networkpolicy` tells you the objects exist; it does not tell you they are enforced. Test it: `kubectl run` a pod in the namespace with a `ploeg-worker` label and try to reach something the worker policy does not list.

Policies add up. Kubernetes takes the union of every policy that selects a pod, so a cluster that already carries a default-deny plus its own allow rules in the namespace (the installer the chart grew up in does) keeps working, and the chart's policies can only widen what an installer allows, never narrow it. The reverse also holds: nothing an installer writes can take back a rule the chart renders. The `extraEgress` and `extraIngress` values exist so an installer puts its additions in one place.

Policy evaluation sees the destination pod and its port, not the Service. Every port below is the port the destination container listens on. The chart derives it from the Service port in the URL, which is right whenever a Service forwards to the same port, and `networkPolicy.<endpoint>.port` sets it when it is not.

## The policies

| Policy | Selects | Ingress | Egress |
| --- | --- | --- | --- |
| `<release>-ploegd` | the ploegd pod (`ploeg.webgrip.dev/component: daemon`) | 8080 from `networkPolicy.ploegd.ingressFrom`; when that is empty, from any peer | DNS; the database; the model gateway (`executor.litellm.adminUrl` and `baseUrl`); every configured forge (`publicUrl` or `url`; `api.github.com` for github.com); every configured tracker; `knowledgeGraph.url`; the Anthropic JWKS when a team runs `claude-environment` |
| `<release>-worker` | every worker pod (`app.kubernetes.io/name: ploeg-worker`), under the `keda`, `cronjob` and `sandbox` executors alike | none | DNS; ploegd on 8080; the model gateway (`executor.litellm.baseUrl`); the executor's forge (`executor.<forge>.url`); every OTLP collector a `telemetry.endpoint` names |
| `<release>-worker-<team>-anthropic` | the worker pods of a team whose Role runs `claude-environment` with a worker as carrier | (egress only) | the internet on 443, outside `privateCidrs` |
| `<release>-sandbox-launcher` | the sandbox launcher pods, rendered when a team runs under the `sandbox` executor | none | DNS; ploegd on 8080; the Kubernetes API on `networkPolicy.kubernetesApi.ports` |
| `<release>-claude-runner` | the claude-runner pods, rendered when `claudeRunner.enabled` | none | DNS; ploegd; every configured forge; the internet on 443; the collector `claudeRunner.env.OTEL_EXPORTER_OTLP_ENDPOINT` names |
| `<release>-mcp` | the ploeg-mcp pods, rendered when `mcp.enabled` | `mcp.port` from `networkPolicy.mcp.ingressFrom`; when that is empty, from any peer | DNS; ploegd; the identity provider `mcp.env.PLOEG_MCP_ISSUER` names |

Each of ploegd, worker, sandbox launcher, claude-runner and ploeg-mcp also takes `networkPolicy.<component>.extraEgress`, and ploegd `extraIngress`, appended as written.

What the table leaves out is deliberate. Workers do not reach the database, the Kubernetes API, the tracker or the knowledge graph. Nothing reaches a worker. The launcher does not reach the model gateway or the forge. Image pulls are the kubelet's, not the pod's, so a policy never affects them; the toolchains of [ADR-0035](../adrs/0035-runs-get-ploeg-owned-skills-mounted-toolchains-and-worker-verification.md) arrive the same way. Kubelet health probes come from the node and are not subject to NetworkPolicy on the CNIs above.

## How the destination of a rule is found

Every URL the chart already holds names a destination. The installer may name it better; when it does not, the chart reads the URL:

| Hostname in the URL | Rendered peer |
| --- | --- |
| an IPv4 address | `ipBlock` of that address, `/32` |
| one label, such as `postgres` or `litellm` | `podSelector: {}`, every pod in the release namespace |
| `<service>.<namespace>.svc` or `<service>.<namespace>.svc.cluster.local` | `namespaceSelector` on `kubernetes.io/metadata.name: <namespace>`: every pod in that namespace, not only the Service's pods |
| anything else, such as `github.com` or `forgejo.example.com` | `ipBlock` `0.0.0.0/0` with `networkPolicy.privateCidrs`, link-local `169.254.0.0/16` and loopback `127.0.0.0/8` excepted |

The port is the URL's, or 443 for `https` and 80 for `http` when the URL names none. Two URLs that resolve to the same peer and port render one rule.

A `.svc` URL admits the whole namespace it names, because a NetworkPolicy selects pods by label and the chart cannot read the Service's selector. A worker whose forge is `http://forgejo-http.forgejo.svc:3000` may reach any pod in `forgejo` that listens on 3000, not only Forgejo's. Set `networkPolicy.<endpoint>.peers` to the Service's pod labels to admit only those pods; the peers then replace the namespace rule for every URL of that kind.

`networkPolicy.<endpoint>.peers` replaces the derived peer for every URL of that kind with a list of `NetworkPolicyPeer` objects, and `networkPolicy.<endpoint>.port` replaces the port. The kinds are `gateway`, `forge`, `tracker`, `knowledgeGraph` and `telemetry`. Name peers when the derivation is too wide, for instance to reach only the gateway's pods in its namespace rather than every pod there:

```yaml
networkPolicy:
  gateway:
    peers:
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: ai
        podSelector:
          matchLabels:
            app.kubernetes.io/name: litellm
```

Two endpoints have no URL to read:

- **The database.** ploegd's connection URI lives in a Secret the chart does not read, so `networkPolicy.database` defaults to every pod in the release namespace on 5432. A database in another namespace, or a CloudNativePG cluster you want to name exactly, sets `peers`.
- **The Kubernetes API.** The API server is not a pod, and the `kubernetes` Service forwards to node addresses the chart does not know. `networkPolicy.kubernetesApi` defaults to any peer on 6443 and 443, which only the launcher policy uses. Narrow it with `peers` naming the API server's endpoint addresses as `ipBlock`s (`kubectl get endpoints kubernetes`), or write your CNI's own form, for Cilium a `CiliumNetworkPolicy` with `toEntities: [kube-apiserver]`.

Two rules reach the internet: a `claude-environment` team's workers and the claude-runner, which log in to and dispatch sessions at Anthropic, and ploegd, which reads the Anthropic JWKS when such a team exists. A NetworkPolicy cannot name a hostname, so these rules allow 443 to every public address. An `http://` public URL does the same on 80. `privateCidrs` keeps the cluster, the nodes and the LAN out of that rule; its default is RFC 1918 plus the shared address space `100.64.0.0/10`, which carrier-grade NAT and some overlay networks use. Add your pod, Service and node ranges when they fall outside these.

Every internet rule also excepts link-local `169.254.0.0/16` and loopback `127.0.0.0/8`, whatever `privateCidrs` holds, so an emptied or replaced list cannot open them. Link-local is where a cloud node's instance metadata service answers (`169.254.169.254`), and its credentials outlive the Run; the harness shares the worker's network namespace, so a worker rule is a harness rule. This is the fix for threat-model finding [F-46](../security/threat-model.md#findings) ([#356](https://github.com/ploeg-hq/ploeg/issues/356)). A URL whose host is itself a link-local address still renders that `/32`, because the installer named it.

DNS is not narrowed by name. Every policy allows UDP and TCP 53 to `networkPolicy.dns.peers`, and the cluster resolver forwards any name it does not serve, so a pod can encode data in the names it looks up. The threat model records that residual; the egress proxy of [#339](https://github.com/ploeg-hq/ploeg/issues/339) is the path that narrows it. A CNI with FQDN policy (Cilium `toFQDNs`) can tighten these to the hosts; the chart does not depend on one.

## Ingress to ploegd

ploegd is reached by workers, launchers, ploeg-mcp and claude-runner from inside the namespace, by the gateway that forwards tracker and forge webhooks, by operator consumers, and by Prometheus. The chart knows the first group and not the others, so `networkPolicy.ploegd.ingressFrom` is empty by default and the rule allows port 8080 from any peer. ploegd authenticates every route of its own, so this is the P1 profile's transport assumption restated, not a widening of it. Set `ingressFrom` to the gateway's and Prometheus's namespaces and the policy allows those peers plus this release's own pods, and nothing else:

```yaml
networkPolicy:
  ploegd:
    ingressFrom:
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: gateway
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: observability
```

The same shape applies to ploeg-mcp through `networkPolicy.mcp.ingressFrom`. The older `mcp.networkPolicy.ingress` and `.egress` values still render their own policies; both forms add up.

## What changes for an existing install

Before this version the chart rendered no policy for ploegd, workers, the launcher or the runner, so an upgrade is the first time the policies apply on a cluster whose CNI enforces them. Three things can stop working:

- **A DinD sidecar.** `harness.dind: true` runs a Docker daemon in the worker pod that pulls images itself. The worker policy does not allow a registry. Add the registry to `networkPolicy.worker.extraEgress`, or move the team off DinD, which the [threat model](../security/threat-model.md#profile-p1-unattended-workers-without-docker-credential-proxies-on) asks for anyway.
- **The registries profile** of [ADR-0037](../adrs/0037-teams-opt-into-registry-egress-through-a-logged-allowlist-proxy.md). Its proxy is the installer's; allow it in `networkPolicy.worker.extraEgress`.
- **A destination the derivation places wrongly.** A two-label in-cluster hostname such as `forgejo.ploeg` reads as a public host, and a Service whose port differs from its pods' renders the wrong port. Use the `.svc` form, or name `peers` and `port`.

The compatibility policy records the change in [breaking-changes.yaml](../contracts/breaking-changes.yaml) against `v0.2.0-rc.47`. `networkPolicy.enabled: false` restores the previous render.

## Checking a rendered policy

```sh
mise exec -- helm template ploeg ops/helm/ploeg -f ops/helm/ploeg/ci/required-values.yaml -f my-values.yaml -s templates/networkpolicy.yaml
```

The golden renders under [`ops/helm/ploeg/ci/golden`](../../ops/helm/ploeg/ci/golden/) carry every policy for the chart's test cases, and [`scripts/test-chart-network-policies.sh`](../../scripts/test-chart-network-policies.sh) asserts each row of the table above on every `mise run verify`, including that `networkPolicy.enabled: false` renders nothing and that an airgapped worker pool has no internet rule.
