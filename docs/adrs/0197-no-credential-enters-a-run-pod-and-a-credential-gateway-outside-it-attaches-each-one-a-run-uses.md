---
status: proposed
date: 2026-11-18
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-03-31
---

# No credential enters a Run's pod, and a credential gateway outside it attaches each one a Run uses

## Context and Problem Statement

[ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md) kept a Run's credentials in `ploeg-worker` and gave the harness placeholders and two loopback proxies ([forgeproxy.go](../../pkg/worker/forgeproxy.go), [llmproxy.go](../../pkg/worker/llmproxy.go)). [ADR-0138](0138-a-harness-runs-in-its-own-container-and-no-credential-enters-it.md) moved the harness into its own container of the same pod. Both records say what they leave: the worker container of every Run pod still holds the team's bootstrap token (`PLOEG_WORKER_BOOTSTRAP_TOKEN` from a `secretKeyRef`, [_helpers.tpl](../../ops/helm/ploeg/templates/_helpers.tpl#L930-L935)), in a single-forge release the shared builder or reader token (`AGENT_BUILDER_TOKEN`, [_helpers.tpl](../../ops/helm/ploeg/templates/_helpers.tpl#L969-L980)), and after the claim the per-Run model key and the per-Run forge credential of [ADR-0192](0192-a-runs-forge-is-resolved-from-its-work-target-and-the-claim-carries-the-forge-and-a-credential-minted-for-that-run.md). The research that led to ADR-0034 already said where this ends: "in a shared cluster, credentials must live outside the Run's pod, in an egress gateway that swaps a per-Run placeholder for the real credential" ([2026-09-28 plan](../research/2026-09-28-credential-isolation-cluster-plan.md), section 4). [ADR-0106](0106-a-run-declares-an-isolation-floor-and-a-cell-runs-it-at-the-strictest-class-it-can-honour.md) names that rung, "credentials outside the Run's pod", for `hardened`, `isolated` and `confidential`, and the [isolation ladder](../research/2026-10-17-isolation-ladder-and-execution-cells.md) lists it as B3, "designed, unbuilt". Nothing renders it today.

Why a pod is not enough:

* Every container of a pod shares its network namespace, and under gVisor or Kata its sandbox or micro-VM. A sandbox escape, a kernel bug in the guest, a privileged DinD sidecar or a mistake in the launcher protocol of ADR-0138 reaches the worker container, and with it every credential of the Run and the standing bootstrap token.
* The fixes of 2026-11-17 to ADR-0192 ([#426](https://github.com/ploeg-hq/ploeg/pull/426)) had to add a deny-list so that a declared Claude Code plugin could not be handed the forge's `botPasswordSecret` or the GitHub App key through the chart ([ADR-0139](0139-a-team-declares-claude-code-plugins-pinned-to-a-commit-whose-hooks-run-guarded-with-only-their-declared-environment.md)). A deny-list in a template is the wrong shape for "never": the next Secret the chart learns about is allowed until someone adds it.
* An installation that runs Runs of several Tenants ([ADR-0178](0178-work-items-shifts-and-runs-take-their-tenant-from-configuration-and-a-consumer-reads-only-its-own.md)) puts one Tenant's forge credential one pod escape away from another Tenant's Run. ADR-0034's own re-evaluation trigger asks for the per-Tenant egress gateway before such a Run.

The decision-maker set the requirement on 2026-11-18 without exception: no secret of any kind is available inside a pod where an agent's harness runs, by any means. Every outside credential a Run needs is attached by a component that runs outside that pod.

Where does that component run, how does it know which Run is calling, what does it let through, where do secrets live, what happens when it fails, and how does an installation get there?

## Decision Drivers

* **Nothing to find.** A harness that controls its whole pod, including the worker's container, its memory and every mounted file, must find no value that authorizes anything outside the pod except the one per-Run value this record allows (section 2), and that value must be worthless anywhere but at its own gateway, for its own Run, until its Run settles.
* **Same fences, new place.** The current forge proxy's rules (own repository, own branch, the delivery contract's routes, the prepare-repository fence of [ADR-0194](0194-a-repository-missing-only-its-instruction-file-may-receive-one-run-that-adds-it.md), the leak watch) are already reviewed and tested. They move; they are not rewritten.
* **One answer for every runtime.** runc, gVisor, Kata and a DinD harness all get the same boundary, because the boundary is the pod's edge, not something inside it.
* **Fail closed, never fall back.** When the gateway is unavailable, Runs wait. No path hands a credential to the pod instead ([ADR-0106](0106-a-run-declares-an-isolation-floor-and-a-cell-runs-it-at-the-strictest-class-it-can-honour.md): degradation is never silent).
* **Least machinery.** No new identity system, no service mesh, no new external dependency. Reuse ploegd's run-bound capability, the ADR-0192 mint path and the chart's NetworkPolicies.
* **One release at a time** ([ADR-0134](0134-ploeg-keeps-each-release-compatible-with-the-one-before-and-removes-only-after-a-deprecated-minor.md)): an existing installation keeps working on upgrade, and the default changes only after a deprecated minor.

## Considered Options

Placement:

* **P1. One credential gateway Deployment per namespace that runs Runs, shipped by the chart**
* P2. A credential gateway DaemonSet, one per node
* P3. An egress sidecar in the Run pod
* P4. Keep the in-worker proxies of ADR-0034 and ADR-0138

How the gateway knows the caller:

* **I1. A per-Run capability issued by ploegd at claim, pinned to the calling pod, behind a NetworkPolicy that makes the gateway the Run pod's only authenticated peer**
* I2. A projected Kubernetes ServiceAccount token in the Run pod
* I3. The calling pod's IP alone, resolved through the Kubernetes API

Where the per-Run forge credential comes from:

* **M1. ploegd mints it as ADR-0192 says and hands it only to the gateway, over the gateway's own authenticated channel**
* M2. The gateway mints it itself

## Decision Outcome

Chosen options: "**P1, I1 and M1**": a credential gateway Deployment in each namespace that runs Runs, which accepts only a per-Run capability ploegd issued at claim, from the pod that claimed it, and attaches credentials it fetched from ploegd and never from the Run pod. It is the only combination in which the Run pod holds no outside credential, no secret is shared with the Run's node by construction, and the fences that exist today keep working unchanged.

Proposed, not built. Each section states the contract the implementation must meet.

### 1. Placement: one gateway per Run namespace

* The chart renders a `ploeg-credential-gateway` Deployment, Service and PodDisruptionBudget in each namespace that runs Runs (the executor namespace, and each cell's Run namespace under [ADR-0107](0107-execution-runs-in-cells-that-pull-from-one-hub.md), where it is that record's "cell gateway"). It runs as its own binary from the ploegd image family, signed and digest-pinned like the others, under the restricted Pod Security standard of [ADR-0120](0120-a-worker-container-meets-the-restricted-pod-security-standard-by-default.md).
* The gateway runs on nodes that run no Run pod. The chart renders a required anti-affinity against every Run pod label and, when the installation names a Run node pool (`nodeSelector` or a taint the Run pods tolerate), a node affinity that excludes it. A node-level escape from a Run, which DinD and a guest-kernel bug both make possible, then reaches a node with no gateway memory on it. An installation with a single node cannot meet this; the chart refuses to render the gateway there unless `credentialGateway.acknowledgeSharedNode: true` is set, and that render labels every Run pod with it.
* A namespace is the unit because a namespace is where the chart already draws the Run boundary: Pod Security level, RBAC, NetworkPolicy and the executor namespace of F-44 are all per namespace. A namespace that serves several Tenants of [ADR-0178](0178-work-items-shifts-and-runs-take-their-tenant-from-configuration-and-a-consumer-reads-only-its-own.md) shares one gateway; an installation that wants a gateway per Tenant gives each Tenant its own Run namespace (section 2 enforces the binding).

Rejected:

* **P2, a DaemonSet per node.** It keeps the gateway close to the Run, and it puts it on the very node a Run escape lands on: every node that runs Runs would hold every Tenant's credentials in use on it. It also scales with nodes, not with Runs.
* **P3, a sidecar in the Run pod.** It shares the pod's network namespace, its sandbox under gVisor and its micro-VM under Kata. A credential in a sidecar is a credential in the pod, which the requirement forbids. This is ADR-0034's rejected option, rejected again for a stronger reason.
* **P4, the in-worker proxies.** They are the current shape and fail the requirement by definition. They stay only as the mode of an installation that has not enabled the gateway (section 6), and are refused in a namespace where it is enabled.

### 2. How the gateway knows which Run is calling

**The capability.** At claim ploegd issues the Run's capability: today's `controlToken`, the HMAC capability already bound to the Run, the worker identity, the Team, the Role and the worker-control audience ([worker-control](../contracts/worker-control.md)), with one more audience, the gateway of the Run's namespace. It is the only bearer value that enters the Run pod. It authorizes:

* at ploegd, the run-scoped worker-control routes of that Run, as it does today;
* at the gateway of that Run's namespace, the forge and model routes section 3 allows for that Run.

It authorizes nothing else. Presented to another namespace's gateway, to the forge, to the model gateway or to any ploegd route outside that Run, it is refused. It stops working when the Run settles, expires or is withdrawn, because the gateway confirms it with ploegd (section 3, grants) and ploegd answers `410` for a settled Run.

This record is explicit about it: the capability is a bearer value inside the pod. Whoever holds it while the Run is live can do, through that one gateway, what the Run itself may do. It cannot be turned into a forge or model credential, because neither ever reaches the pod, and it is dead after the Run. That is the residual this design accepts, and it is the same authority a prompt-injected harness already has by running.

**Pinned to the pod that claimed.** The claim itself passes through the gateway (below). The gateway records the claiming pod's name, UID and IP with the capability, and afterwards serves the capability only from that pod's IP while a pod of that UID still holds it. A capability copied out of the pod is refused everywhere, including from a sibling Run pod in the same namespace.

**The claim without a bootstrap token in the pod.** The Run pod no longer carries `PLOEG_WORKER_BOOTSTRAP_TOKEN`. The worker sends its claim to the gateway with its worker identity (`X-Ploeg-Worker-ID`, the pod name). The gateway resolves the source IP to a pod through the Kubernetes API (get, list and watch Pods in its own namespace, nothing else), checks that the pod is Running, that its name equals the worker identity, that it is owned by a Job or Sandbox the chart renders, and reads its `ploeg.webgrip.dev/team` and `ploeg.webgrip.dev/role` labels. It then forwards the claim to ploegd with the bootstrap capability for that Team and Role, which only the gateway holds. ploegd answers as today; the gateway records the pin and passes the capability back. A harness has no way to change its pod's labels: the pod mounts no ServiceAccount token.

**The network.** NetworkPolicy makes the gateway the Run pod's only peer besides DNS: Run pod egress allows the gateway Service and cluster DNS, nothing else. The gateway's ingress allows only pods carrying the Run pod labels in its namespace, and its egress only ploegd, the configured forges, the configured model gateway and the Kubernetes API. The worker's own worker-control calls to ploegd go through the gateway as a pass-through that attaches nothing, so ADR-0106's "the egress gateway is the only peer" holds for every class. Any other egress a team opted into (the registry allowlist proxy of [ADR-0037](0037-teams-opt-into-registry-egress-through-a-logged-allowlist-proxy.md), telemetry) must be unauthenticated from the pod's side; the chart refuses to render a credential for it in a Run pod.

Rejected:

* **I2, a projected ServiceAccount token in the Run pod.** It is a file on a volume the harness reads, valid for the Kubernetes API audience or any audience it names, for as long as the pod lives; [ADR-0138](0138-a-harness-runs-in-its-own-container-and-no-credential-enters-it.md) already rejected it for the bootstrap, and TB2 rests on `automountServiceAccountToken: false` ([_helpers.tpl](../../ops/helm/ploeg/templates/_helpers.tpl#L697)). It would also be a secret in the pod, which the requirement forbids.
* **I3, pod IP alone.** It would put no bearer value in the pod at all, which is its strength. It is the claim-time check above. As the only check for every request it depends on the CNI refusing spoofed source addresses, on the gateway's view of pod IPs never lagging an IP's reuse, and on no node-local proxy or mesh rewriting source addresses. Combined with the capability those become a second factor, and a failure of either alone leaks nothing.

### 3. What passes through the gateway

The gateway has three route families, and answers `403` with a named reason to everything else:

* **Forge.** git smart-HTTP and the forge API of the Run's own forge, restricted exactly as the current forge proxy restricts them: the Run's own repository only, the Role's allowlist of delivery-contract routes ([forgeproxy.go](../../pkg/worker/forgeproxy.go) `forgejo*Endpoints`, `gitlab*Endpoints`, `github*Endpoints`), pushes fenced to `refs/heads/<run branch>` without deletion ([pushfence.go](../../pkg/worker/pushfence.go)), the prepare-repository fence of ADR-0194 ([prepare_deliverable.go](../../pkg/worker/prepare_deliverable.go)), the path normalisation that refuses dot segments, encoded separators and double encoding, and the leak inspection that refuses an API request carrying one of the Run's real credential values or its canary ([leakwatch.go](../../pkg/worker/leakwatch.go)). The code moves into a package both the worker fallback and the gateway import, so there is one implementation of each fence.
* **Model.** Requests to the model gateway configured under [ADR-0177](0177-ploeg-reaches-models-through-a-gateway-contract-with-litellm-and-envoy-ai-gateway-as-implementations.md), with the per-Run key attached, streaming unchanged. MCP tools reach the Run only through that gateway ([ADR-0078](0078-runs-reach-read-only-mcp-tools-only-through-the-gateway-bounded-by-a-litellm-team.md)), so they pass here and nowhere else.
* **ploegd.** The worker-control routes of the Run's own capability, and the claim (section 2). The gateway forwards them without attaching anything except the bootstrap capability on a claim.

**Grants: ploegd mints, the gateway fetches, the pod never sees.** ploegd keeps minting the per-Run forge credential (ADR-0192, [ADR-0013](0013-push-rights-are-minted-per-run.md), [ADR-0086](0086-github-is-a-forge-and-a-run-pushes-with-its-own-github-app-installation-token.md), [ADR-0172](0172-the-forgejo-broker-runs-only-on-forgejo-15-and-refuses-a-token-the-forge-did-not-restrict-to-the-runs-repository.md)) and the per-Run model key (ADR-0177) at claim. In a gateway namespace the claim response carries neither; it carries the capability, the forge dialect, the gateway's URLs for the forge and the model, and a placeholder flag. On a capability's first use, and before a credential's expiry, the gateway asks ploegd for the Run's grant on a new internal route, `POST /api/v1/credential-gateway/grants`, authenticated as the gateway (below), presenting the capability. ploegd answers only when the capability is valid, its Run is live and the Run's executor namespace is the asking gateway's. The grant holds the forge dialect and URL, the repository, the access (read or write), the Run branch, the deliverable fence, the forge credential and its expiry, the model gateway URL and the per-Run key. The gateway keeps grants in memory only, for at most the credential's expiry or 60 seconds, whichever is shorter, and refetches; a `410` drops the grant at once. GitHub installation-token refresh, which the worker does today through `POST /api/v1/runs/{token}/forge-credential`, becomes the gateway's refetch.

**The gateway's own identity.** The gateway authenticates to ploegd with a projected ServiceAccount token for the audience `ploegd`, with a ten-minute expiry, which ploegd verifies through `TokenReview` and maps to the gateway's namespace. That namespace is the binding ploegd checks a grant against, and the configuration that binds a namespace to the Tenants it may run is where ADR-0178's Tenant meets this record: ploegd refuses a grant for a Run whose Tenant the asking gateway's namespace is not bound to. No long-lived gateway secret exists.

Rejected **M2, the gateway mints itself**: it would put the Forgejo bot password, the GitHub App key and the model gateway's admin authority into every Run namespace, which multiplies the most powerful secrets of the installation by the number of namespaces, and splits ADR-0192's one mint path in two.

### 4. Where secrets live

* **ploegd** holds every root credential: the forge bot password, the GitHub App key, the model gateway's admin authority, connection secrets through the secret store of [ADR-0184](0184-tracker-and-forge-connection-secrets-live-in-a-secret-store-ploeg-writes-itself-and-no-person-handles-them.md), the bootstrap registry and the signing key, as today.
* **The gateway** holds the bootstrap capabilities of the Teams and Roles its namespace runs (from a Secret mounted only into the gateway), its own projected token, and in memory the grants of the Runs it currently serves. It holds no root credential.
* **The Run pod** holds none of these. Every container of it, the worker and the harness alike, has no `secretKeyRef`, no `secret` or `projected` volume, no credential in its environment and `automountServiceAccountToken: false`. The only bearer value in it is the capability of section 2.
* **The worker becomes credential-free.** It keeps the claim loop, the launcher of ADR-0138, the board watch, verification and the leak scan, and it reaches git, the forge API and the model only through the gateway with the capability, exactly like the harness. Its loopback model observer stays as a key-less relay that only measures activity ([llmproxy.go](../../pkg/worker/llmproxy.go) `startLLMObserver`). Decided: the worker's run-scoped claim token and the gateway capability are the **same** value. A second value would sit in the same container, be revoked at the same moment and be pinned to the same pod, so it adds a thing to rotate and no boundary.
* **Everything else that puts a secret in a Run pod is refused in a gateway namespace**: the static `LLM_API_KEY` of `workerAuth.mode: legacy`, `tokenSecret` and `readTokenSecret` fallbacks, `PLOEG_CLAUDE_PLUGIN_ENV_*` from a Secret (ADR-0139; the deny-list of #426 becomes an allow-list of none), and any `extraEnv` or volume that names a Secret. The chart fails the render naming the value. A `claude-environment` workload runs no harness in its pod (ADR-0138, item 5) and holds a person's login; it is out of this record's scope and must not run in a gateway namespace.

### 5. Failure, audit and limits

* **Gateway down.** No claim can be made without the gateway, so new Runs stay `pending` and count as capacity waits in the demand signal of [ADR-0072](0072-keda-stays-and-ploegd-reserves-a-run-for-each-launcher.md), reported on the Work Item as `credential_gateway_unavailable`. A Run already running retries the gateway with backoff while its Lease holds; when the Lease would lapse it ends `stuck` with that reason and ploegd revokes its credentials as for any settled Run. Nothing falls back to a credential in the pod: in a gateway namespace ploegd never puts one in a claim response, and the worker refuses to start if it finds a credential variable or a Secret mount.
* **Several replicas.** The gateway is stateless apart from its grant cache and the claim pins. It runs two replicas by default with a PodDisruptionBudget of one and topology spread over nodes. Pins are written to ploegd with the claim (the claim carries the pod name, UID and IP the gateway saw), so any replica can check a pin by fetching the grant. A streaming model call cut by a replica's loss fails; the harness's own retry reaches another replica.
* **Audit.** The gateway writes one structured record for every request that carried an attached credential and for every refusal: Run, Work Item, Tenant, credential kind, credential identity (the token id or key alias, never the value), route family, method, the matched route pattern, status, bytes and duration. It sends them in batches to ploegd, which stores per-Run totals with the Run and keeps every refusal as an audit row (`credential_gateway.refused`) under the Run's Tenant. The full per-request record stays in the gateway's log stream.
* **Limits.** Per Run, a token bucket on requests and on request bytes per route family, with defaults in the chart and an override per Team. Per namespace, a cap on concurrent upstream connections. A refused request answers `429` with the Run's limit named; an upstream `429` passes through unchanged.

### 6. Migration and harness qualification

1. **Opt-in.** `executor.credentialGateway.enabled` (default `false`) renders the gateway and switches every Run workload of that namespace to gateway mode. ploegd learns a namespace's mode from its configuration and refuses to issue an in-pod credential to a worker of a gateway namespace. The in-worker proxies of ADR-0034 and ADR-0138 remain for installations that leave it off.
2. **Default.** After the gateway has run on the maintainers' installation for every harness and every class for one stable minor, the chart default becomes `true` in a `feat!` release.
3. **Retire.** The in-worker proxy mode is marked `DEPRECATED` with the default flip and removed one stable minor later, per ADR-0134. Removing it removes `PLOEG_LLM_KEY_ISOLATION`, `PLOEG_FORGE_TOKEN_ISOLATION` and the credential paths in the worker.

**Qualification changes.** ADR-0034 qualified a harness per proxy because a harness had to reach a loopback address in its own container. With the gateway, a harness qualifies when it sends its model calls to a configurable base URL and its git and forge API calls over HTTP to a configurable URL, which every qualified harness already does. A DinD harness qualifies too, because the gateway is a cluster Service its inner containers reach through the pod's network, and they receive the capability exactly as the harness does; the anti-affinity of section 1 is then required, not advisory. ADR-0034's three acceptance conditions move to the gateway: refusal of a request without the Run's capability (it was the placeholder), the branch and route fence, and the `CAP_SYS_PTRACE` refusal, which in gateway mode guards nothing secret and stays only as a launcher precondition of ADR-0138.

### Consequences

* Good, because the requirement holds by construction: a harness that owns its whole pod, every container, every mount and every byte of memory, finds no forge credential, no model key, no bootstrap token and no ServiceAccount token, under every runtime class, DinD included.
* Good, because the fences that exist today move unchanged into one shared implementation, so the review and tests behind them carry over.
* Good, because ADR-0106's `hardened`, `isolated` and `confidential` classes get the credential column they promise, and ADR-0107's cell gateway is this component.
* Good, because the deny-list of #426 becomes unnecessary: in a gateway namespace no Secret reaches a Run pod, whichever Secret it is.
* Good, because every use of a credential is recorded per Run in one place, outside the Run.
* Bad, because a new component with its own replicas, NetworkPolicy, RBAC (Pods read in its namespace) and image must stay available for any Run to start. Accepted: a down gateway makes Runs wait, which is the failure mode the requirement asks for.
* Bad, because ploegd gains `TokenReview` permission and a new internal route, and the claim contract changes for gateway namespaces.
* Bad, because every byte a Run sends to the forge and the model now crosses the network twice, and model streaming depends on one more hop. Expected cost is small next to model latency; the e2e test of the Confirmation measures it.
* Bad, because the per-Run capability is still a bearer value in the pod. Pinning and expiry make it worthless outside the pod and after the Run, but inside a live Run it carries that Run's authority. No design that lets the harness act can remove that.
* Bad, because a single-node installation cannot keep the gateway off the Run's node; it must acknowledge that explicitly.
* Neutral, because `claude-environment` stays outside this record and must not share a gateway namespace.

### Confirmation

Proposed, none of these exist yet. Once built, `mise run verify` in `.github/workflows/ci.yml` runs them:

* **Chart goldens.** `ops/helm/ploeg/ci/golden/executor-credential-gateway.yaml` renders the gateway for a namespace with a writer, a reader, a gVisor and a Kata team and a sandbox template. `scripts/helm-golden.sh check` asserts, for every Run pod template in it (ScaledJob, CronJob and SandboxTemplate), that no container has a `valueFrom.secretKeyRef` or `envFrom.secretRef`, that the pod has no `secret` or `projected` volume, that `automountServiceAccountToken` is `false`, and that no environment variable named `AGENT_BUILDER_TOKEN`, `LLM_API_KEY`, `PLOEG_WORKER_BOOTSTRAP_TOKEN` or `PLOEG_CLAUDE_PLUGIN_ENV_*` exists; that the Run pods' NetworkPolicy allows egress only to DNS and the gateway; and that the gateway carries the bootstrap Secret and the anti-affinity. Render refusals: gateway on with `workerAuth.mode: legacy`, with a plugin variable from a Secret, with an `extraEnv` naming a Secret, and on one node without the acknowledgement.
* **Gateway e2e with fakes.** `go test ./pkg/credentialgateway/` runs the gateway against `httptest` fakes of ploegd (claim and grants), a Forgejo, a GitHub and a GitLab forge, and a model gateway. It proves that the forge receives the minted credential and the model gateway the per-Run key; that a request without a capability, with another Run's capability, from another source pod, after the Run settled, or at the gateway of another namespace is refused and never reaches a fake; that each fence of today's forge proxy tests holds through the gateway (the existing `TestForgeProxyRefusesEverythingOutsideTheRunsRepository`, push-fence and prepare-fence cases run against both the worker fallback and the gateway); and that a fake ploegd that is down leaves claims pending and a running Run `stuck`, never served.
* **Claim contract.** `pkg/httpapi` tests that a claim from a gateway namespace carries no forge credential and no model key, that a grant is refused to a gateway of another namespace or a Tenant it is not bound to, and that a settled Run's grant answers `410`.
* **Hostile harness.** `TestRunPodHoldsNoCredential` in `pkg/worker` runs the worker and an `exec` harness in gateway mode against the fakes, with fixture values for the bootstrap capability, the forge credential and the model key. The harness reads its environment, every readable `/proc/*/environ`, `/proc/*/cmdline` and `/proc/*/mem` it can open, every file under its mounts, `/var/run/secrets/kubernetes.io/serviceaccount` and the worker's container filesystem through the shared volumes, and the test asserts it finds none of the three values. The same probe joins the attack drill of [ADR-0126](0126-a-recurring-attack-drill-runs-every-harness-against-a-hostile-repository.md) against a real pod on a kind cluster.
* `go test ./internal/ledger/` validates this record and `go test ./internal/boundary/` keeps it free of consumer names.

## Pros and Cons of the Options

### P2. A DaemonSet per node

* Good, because traffic stays on the node and scales with node count.
* Bad, because a Run escape to the node meets the gateway's memory on that node.
* Bad, because every node holds the credentials of whichever Tenants' Runs it hosts.

### P3. An egress sidecar in the Run pod

* Good, because no new network hop and no cluster Service.
* Bad, because the credential is in the pod, in its network namespace and its sandbox or VM.

### P4. Keep the in-worker proxies

* Good, because it exists, is qualified and costs nothing more.
* Bad, because the worker container, and with it the bootstrap token and every per-Run credential, is one container or sandbox escape from the harness.

### I2. A projected ServiceAccount token in the Run pod

* Good, because Kubernetes rotates and audiences it.
* Bad, because it is a readable file in the pod for the pod's life, and a secret by the requirement's definition.

### I3. Pod IP alone

* Good, because the pod holds no bearer value at all.
* Bad, because it rests on CNI anti-spoofing and on an IP-to-pod view that can lag reuse, with nothing behind it when either fails.

### M2. The gateway mints

* Good, because ploegd is not in the per-request path at all.
* Bad, because the root forge and model credentials are copied into every Run namespace.

## Re-evaluation triggers

* kubernetes-sigs/agent-sandbox, a CNI or a service mesh ships per-pod credential injection outside the pod that can bind a different credential per Run and enforce the forge fences: compare it with this gateway.
* A harness that cannot be pointed at an HTTP base URL for the model or the forge is wanted.
* The gateway's added latency exceeds 10% of a Run's model time in the e2e measurement or in production, or a gateway outage makes Runs wait more than once a month.
* A Tenant needs its credentials in a cluster of its own: a gateway per cell of ADR-0107 is the next step, and the zero-secret cell mode of that record.
* The decision-maker relaxes or sharpens the requirement of 2026-11-18, for example to forbid the capability itself (then I3 becomes the only identity).

## More Information

* Requirement: stated by the decision-maker on 2026-11-18: no secret of any kind is available in a pod where an agent's harness runs.
* Evidence: [2026-09-28 credential isolation plan](../research/2026-09-28-credential-isolation-cluster-plan.md) section 4, [2026-10-04 isolation qualification](../research/2026-10-04-isolation-qualification.md), [2026-10-17 isolation ladder](../research/2026-10-17-isolation-ladder-and-execution-cells.md) rung B3, [2026-10-23 credentials out of the harness container](../research/2026-10-23-credentials-out-of-the-harness-container.md).
* Builds on [ADR-0013](0013-push-rights-are-minted-per-run.md), [ADR-0016](0016-forge-registry-and-per-run-repo-scoped-credentials.md), [ADR-0034](0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md), [ADR-0106](0106-a-run-declares-an-isolation-floor-and-a-cell-runs-it-at-the-strictest-class-it-can-honour.md), [ADR-0138](0138-a-harness-runs-in-its-own-container-and-no-credential-enters-it.md), [ADR-0177](0177-ploeg-reaches-models-through-a-gateway-contract-with-litellm-and-envoy-ai-gateway-as-implementations.md), [ADR-0192](0192-a-runs-forge-is-resolved-from-its-work-target-and-the-claim-carries-the-forge-and-a-credential-minted-for-that-run.md) and [ADR-0194](0194-a-repository-missing-only-its-instruction-file-may-receive-one-run-that-adds-it.md). Related: [ADR-0078](0078-runs-reach-read-only-mcp-tools-only-through-the-gateway-bounded-by-a-litellm-team.md), [ADR-0107](0107-execution-runs-in-cells-that-pull-from-one-hub.md), [ADR-0178](0178-work-items-shifts-and-runs-take-their-tenant-from-configuration-and-a-consumer-reads-only-its-own.md), [ADR-0184](0184-tracker-and-forge-connection-secrets-live-in-a-secret-store-ploeg-writes-itself-and-no-person-handles-them.md).
* When accepted and built, ADR-0034 and ADR-0138 gain a More Information line pointing here; their decisions stay as the fallback mode until section 6 retires it.
* Open for the decision-maker: whether the per-Run capability is acceptable under the requirement or must be replaced by pod-IP identity alone; whether the gateway's own identity to ploegd is a projected ServiceAccount token (recommended) or a mounted Secret; whether `claude-environment` gets its own record; and whether a single-node installation may run the gateway at all.
* 2026-11-18: proposed. Nothing built.
