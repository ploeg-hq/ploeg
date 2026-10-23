---
status: proposed
date: 2026-10-23
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# The chart renders a default-deny NetworkPolicy for every pod, with an allowlist derived from its URLs

## Context and Problem Statement

The threat model's finding F-02 (High, release-blocking) says the chart renders no NetworkPolicy for ploegd, worker pods, the sandbox launcher or the claude-runner; only `mcp.yaml` and the sandbox template carry one, and both are opt-in. A worker's egress is whatever the cluster allows by default, so a prompt-injected harness can reach any service the cluster network reaches ([T-HN-5](../security/threat-model.md#findings), [issue #223](https://github.com/ploeg-hq/ploeg/issues/223)). The airgapped worker of [ADR-0037](0037-teams-opt-into-registry-egress-through-a-logged-allowlist-proxy.md) existed only where an installer wrote it. What does the chart render by default, how does it learn where the gateway, forge, tracker and database are, and how does it stay out of the way of an installer that already has policies?

## Decision Drivers

* A default install must put every Ploeg pod under a policy that denies by default, without the installer writing one.
* The chart already holds the URLs of everything a pod calls. Asking the installer to repeat them as selectors is a second source of truth that drifts.
* NetworkPolicies are additive. A chart policy can never narrow an installer's, so the chart must not depend on the installer's absence, and the installer must not depend on the chart's.
* The chart must not assume a CNI. The homelab that installs it runs Cilium; the quickstart runs kind with kindnet, which enforces nothing.
* An installer must be able to name an endpoint exactly, as a namespace and pod selector or as a CIDR and port, in the shape `mcp.networkPolicy` already uses.
* This changes the behaviour of an existing install on an enforcing CNI, so it falls under the [compatibility policy](../ops/compatibility.md).

## Considered Options

* One policy per pod kind, `policyTypes: [Ingress, Egress]`, with the allowlist derived from the chart's URLs and overridable per endpoint, on by default
* One namespace-wide default-deny plus per-kind allow policies
* Opt-in policies with required peers, as `mcp.networkPolicy` does today
* Leave the network to the installer and document the required paths

## Decision Outcome

Chosen option: "**One policy per pod kind, derived from the chart's URLs, overridable per endpoint, on by default**", because it is the only option that makes a default install deny by default without a second source of truth, and because a per-kind policy is the unit a reviewer and a test can reason about.

1. `templates/networkpolicy.yaml` renders one NetworkPolicy for ploegd, one for every worker pod (the `keda`, `cronjob` and `sandbox` executors share the `ploeg-worker` label), one for the sandbox launcher when a team runs under the sandbox executor, one for claude-runner and one for ploeg-mcp when they are enabled, and one egress-only policy per team whose workers carry `claude-environment` Runs. Each lists `Ingress` and `Egress` in `policyTypes`; what is not listed is denied.
2. The egress of each rule is read from the URL the chart already renders into the pod's environment: an IP is itself, a one-label host is the release namespace, `name.ns.svc` is that namespace, anything else is the internet outside `networkPolicy.privateCidrs`, on the URL's port or the scheme's default. `networkPolicy.{gateway,forge,tracker,knowledgeGraph,telemetry}.peers` and `.port` replace the derivation for every URL of that kind. The database and the Kubernetes API have no URL the chart can read and take peers and ports directly, with namespace-wide and port-only defaults.
3. ploegd's ingress and ploeg-mcp's ingress allow their port from any peer until the installer names `ingressFrom`, because the gateway, operator consumers and Prometheus are the installer's; named, the policy allows those peers plus the release's own pods.
4. `networkPolicy.enabled: false` renders the previous chart. The existing `mcp.networkPolicy` and `executor.sandbox.networkPolicy` keep rendering as they did.
5. The rows of the policies are a table in [docs/ops/network.md](../ops/network.md), and the compatibility ledger records the change against `v0.2.0-rc.47`.

### Consequences

* Good, because a default install on an enforcing CNI has every Ploeg pod under default deny, and a worker reaches DNS, ploegd, the model gateway and the forge and nothing else.
* Good, because an installer that already carries policies loses nothing: the union is at least what it allowed, and the `extraEgress` values give its additions one place.
* Good, because the derivation follows the URL values, so moving the gateway to another namespace moves the rule with it.
* Bad, because a NetworkPolicy cannot name a hostname, so the rules for Anthropic, a public forge or a public identity provider allow 443 to every public address. Accepted: the alternative is a CNI-specific object the chart cannot assume. `privateCidrs` keeps the cluster and the LAN out of it, and an installer with FQDN policy tightens it.
* Bad, because the Kubernetes API rule of the launcher is port-only by default. Accepted: the API server is not a pod, and the launcher is the one pod with a token, scoped to `sandboxclaims`.
* Bad, because a DinD sidecar's own image pulls and the ADR-0037 proxy now need `networkPolicy.worker.extraEgress`. Accepted: both are outside the supported profile.
* Bad, because the derivation reads a Service port as the pod port and a two-label host as public. Accepted and documented; `port` and `peers` correct it.
* Neutral: on kind with kindnet the objects exist and enforce nothing. The quickstart says so and keeps them, so its render is the production render.

### Confirmation

* `scripts/test-chart-network-policies.sh`, run by `mise run verify`, renders the default, executor, sandbox, claude-runner, mcp and GitHub cases and asserts each policy exists, selects its pod, denies ingress where it must, carries each derived rule, carries no internet rule for an airgapped worker pool, deduplicates two URLs on one peer, honours named `peers`, `port`, `ingressFrom` and `extraEgress`, and renders nothing with `networkPolicy.enabled: false`.
* `scripts/helm-golden.sh check`, in the same job, pins every rendered policy byte for byte in `ops/helm/ploeg/ci/golden/`.
* `go test ./internal/compat/` holds the `breaking-changes.yaml` entry to this record.

## Pros and Cons of the Options

### One namespace-wide default-deny plus per-kind allow policies

* Good, because one object says "deny" for the whole namespace.
* Bad, because the chart would select pods it did not create, including the installer's database, and a namespace-wide policy from a chart is exactly the conflict the additive model cannot resolve.

### Opt-in policies with required peers

* Good, because every rule is explicit and the chart derives nothing.
* Bad, because a default install still renders nothing, which is the finding.

### Leave the network to the installer

* Good, because the installer knows its CNI.
* Bad, because the threat model's residual for T-HN-5 stays High and the supported profile exists only on paper.

## Re-evaluation triggers

* The chart or its installers adopt a CNI-specific policy type (Cilium, Calico) for FQDN rules, so the internet rules can name hosts.
* Kubernetes network policy gains a cluster-level or FQDN form (the `AdminNetworkPolicy` API reaching GA, or an FQDN selector) that the chart can render instead.
* A NetworkPolicy rendered here breaks an installer that already carries policies, which means the additive assumption failed somewhere.
* The ADR-0037 registries profile lands in the chart, which should render its proxy rule here rather than through `extraEgress`.
* Five installers set `networkPolicy.enabled: false`, which means the defaults are wrong rather than strict.

## More Information

* [docs/ops/network.md](../ops/network.md) lists every policy and the values that shape it.
* [Threat model](../security/threat-model.md) F-02, T-HN-5, TB2; [issue #223](https://github.com/ploeg-hq/ploeg/issues/223).
* Related: [ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md), [ADR-0037](0037-teams-opt-into-registry-egress-through-a-logged-allowlist-proxy.md), [ADR-0120](0120-a-worker-container-meets-the-restricted-pod-security-standard-by-default.md), [ADR-0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md).
* 2026-10-23: proposed with the templates, tests, goldens and documentation.
