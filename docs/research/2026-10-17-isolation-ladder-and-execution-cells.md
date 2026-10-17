# The isolation ladder, and execution cells for agents at scale

Date: 2026-10-17 · Ticket: none yet · Decision records: none yet. This dossier proposes two:
"a Run declares an isolation class and a cell advertises what it can honour" and "execution
runs in cells that pull from one hub". It continues
[2026-09-28-credential-isolation-cluster-plan.md](2026-09-28-credential-isolation-cluster-plan.md)
and answers its open question 3, the tenancy unit.

## Why this came up

The owner wants to run Ploeg and Unfold on capacity outside the homelab, and wants "agents at
scale across clusters" to be a selling point of Ploeg. Before designing that, the owner asked
what security postures the plane has today, why each was chosen, which is the weakest and the
strongest, and how to move between them without rediscovering the dind lesson of
[homelab ADR-0053](https://github.com/webgrip/homelab-cluster/blob/main/docs/techdocs/docs/adr/adr-0053-daemonless-agent-plane.md):
a posture that lives in five files, one of them a chart default nobody wrote down, is not a
posture.

Every fact in sections 1 to 3 was read on 2026-10-17 from the homelab-cluster tree at
`788a4af4` (`main`) and the live homelab cluster (read-only `kubectl`). The Ploeg facts were
first read at `f81c929` and then re-checked against `development` at `6269dc1` the same day,
which is 206 commits later; where the two differ, this record says so. Sections 4 to 7 are a
proposal.

## 1. What is deployed today

The homelab runs four teams. Two shapes exist side by side.

| Layer | bronze, copper | silver, pewter | Unfold workspaces | Decided by |
| --- | --- | --- | --- | --- |
| Execution boundary | Kata micro-VM via the agent-sandbox executor (`executorType: sandbox`, `runtimeClassName: kata`) | runc, plain KEDA ScaledJob pod | runc, PSA `restricted` namespace | sandbox executor: OpenSpec `sandbox-executor`; Kata substrate: homelab rfc-ci-isolation-talos step 7. No ADR names Kata |
| Privileges | `dind: false`, PSA `enforce: baseline`, Kyverno denies `shareProcessNamespace` and `SYS_PTRACE` | same | `restricted` | [homelab ADR-0053](https://github.com/webgrip/homelab-cluster/blob/main/docs/techdocs/docs/adr/adr-0053-daemonless-agent-plane.md), homelab policy `agent-plane-process-isolation` |
| Worker container `securityContext` | none rendered by the chart; `agent-runner` has no `USER`, so the harness runs as root | same | chart-owned | gap G2 of the 2026-09-28 plan, still open |
| Credentials | managed worker auth; in-worker proxies for the model key and the forge token; per-Run model keys with caps; per-Run repo-scoped forge tokens; no SA token | same | operator token, `maxBudgetUsd: 1.00` | [ADR-0034](../adrs/0034-the-harness-gets-placeholders-the-worker-keeps-credentials.md) (accepted 2026-10-04 for single-tenant Runs without DinD), [ADR-0012](../adrs/0012-two-level-budgets-authorized-and-settled.md), [ADR-0013](../adrs/0013-push-rights-are-minted-per-run.md), [ADR-0025](../adrs/0025-management-authority-stays-in-the-control-plane.md) |
| Network | default-deny plus DNS; egress to the whole `ploeg` namespace, LiteLLM `:4000`, Forgejo `:3000`, Vikunja `:3456`; LAN, gateways and internet refused and probed | same; pewter workers may also reach Anthropic hosts directly | default-deny; egress to Unfold, LiteLLM, Forgejo and the entire gateway namespace with no port filter | [homelab ADR-0006](https://github.com/webgrip/homelab-cluster/blob/main/docs/techdocs/docs/adr/adr-0006-default-deny-network-policies.md); [ADR-0037](../adrs/0037-teams-opt-into-registry-egress-through-a-logged-allowlist-proxy.md) is accepted but the chart renders none of it |
| Authority in the forge | readers: read-only token; writers: per-Run token scoped to one repository; the branch fence and route allowlist (VIK-1475) are merged on `development` and ride the rc train the homelab runs; Forgejo branch protection stays the backstop | same | same | ADR-0013 tier 2 |
| Authority in Kubernetes | none for workers (`automountServiceAccountToken: false`); the sandbox launcher may create, get and delete `sandboxclaims` in its namespace | none | Vloer provisioner creates workspace pods | [executor contract](../contracts/executor.md) |
| Placement | hard `nodeSelector pool=worker`; Kata additionally needs the node capability label | same | Kyverno mutation to `pool=worker` | [homelab ADR-0002](https://github.com/webgrip/homelab-cluster/blob/main/docs/techdocs/docs/adr/adr-0002-application-workload-placement.md) |
| Spend | pool per Shift, cap per Role, `maxFixRounds`; authorize before, settle after | same | per-consumer budget | ADR-0012, [ADR-0032](../adrs/0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md) |
| Detection | key-leak, budget-blocked, worker-failure, lease-stuck and queue alerts; standing probe Jobs for Kata reachability and worker egress | same | none specific | homelab `slo-dark-factory` rules |

Three observations fall out of the table.

1. **One chart default still sits below the estate's posture.** On `development`,
   `keyIsolation`, `forgeTokenIsolation` and `workerAuth.mode` default to `proxy`, `proxy`
   and `managed` since VIK-576 (`a1ffca6`), backed by
   [2026-10-04-isolation-qualification.md](2026-10-04-isolation-qualification.md). But
   `executor.harness.dind` still defaults to `true` (`ops/helm/ploeg/values.yaml:251`),
   renders a `privileged: true` sidecar, and switches both proxies off for that workload with
   only a logged reason. A new installation that leaves it alone is in the shape ADR-0053
   retired, with its credentials exposed as a side effect.
2. **The runtime boundary and the credential boundary are not aligned.** bronze runs in a
   VM but its credentials sit inside that VM, next to the harness (ADR-0034 names Kata as a
   trigger for moving them out, section 4 of the 2026-09-28 plan). silver runs with the same
   credential model and no VM at all. The strongest runtime and the weakest runtime share
   one credential posture.
3. **Egress is namespace-shaped, not workload-shaped.** The homelab's `ploeg-allow-egress`
   selects every pod, so a worker may open `ploeg-db:5432`. Unfold workspaces may reach every
   host the gateway serves. The probe asserts what is blocked, not what is reachable inside
   the allowed set.

## 2. What each layer defends against

| Adversary or failure | Asset at risk | Layer that holds today | Layer that would hold better |
| --- | --- | --- | --- |
| Malicious or compromised code in the target repository, run by the agent (`npm ci`, tests, build scripts) | the node, every other pod on it, the cluster | Kata for bronze and copper; for silver and pewter only runc plus baseline PSA and the process-isolation policy | a VM or user-space kernel for every writing and reading Run, since both execute repository code |
| Prompt injection through ticket text, repository content or review comments | model budget, forge write access, data in the Run | per-Run key caps and pools; per-Run repo-scoped token; placeholders in the harness; egress allowlist | placeholder checks at the proxies, a branch fence, credentials outside the pod |
| A buggy or hostile harness | credentials in its process space | non-dumpable worker, environment allowlist, `SYS_PTRACE` denied | credentials outside the pod |
| Noisy neighbour, runaway Run | node memory and CPU, other teams' Runs | Guaranteed QoS, Kata overhead accounted, team caps, lease and harness timeouts | per-cell `maxRunning`, dedicated node pools |
| Cross-tenant reach, once more than one customer runs | another tenant's repositories, keys and output | nothing; the plane is single-tenant by construction | a cell per trust zone, a namespace per team inside it, credentials swapped outside the Run |
| An operator inheriting a default they did not write | the whole posture, silently | estate overrides, Kyverno denies, the dind re-enable needs three named things | safe chart defaults; one declared class validated at render and enforced at admission |
| Lost or stale control over which posture a team runs | the ability to answer "what posture does bronze have" | reading six files | a label on every Run pod carrying its class, alerted on drift |

## 3. The ladder

A posture is a point on six axes, not one number. Each axis is ordered from weakest to
strongest, and each rung names what it needs. "Today" marks the homelab.

#### A. Execution boundary: what stands between the Run and the node

| Rung | Shape | Needs | Status |
| --- | --- | --- | --- |
| A0 | privileged container or DinD sidecar | a PSA carve-out and a Kyverno waiver | retired by ADR-0053; still the chart default |
| A1 | runc, PSA `baseline` | nothing | **today**: silver, pewter |
| A2 | runc, PSA `restricted`: non-root, drop ALL, no privilege escalation, seccomp, read-only root | a non-root image and a worker `securityContext` knob (gap G2) | Unfold workspaces today; blocked for workers by the root `agent-runner` image |
| A3 | A2 plus user namespaces (`hostUsers: false`), so root inside is not root outside | Kubernetes 1.33 or later with a runtime that maps ids | proven on this cluster (a freshrss pod runs with `hostUsers=false`); zero overhead; works on any cloud VM |
| A4 | gVisor (`runsc`, systrap): a user-space kernel | the Talos `gvisor` extension and a RuntimeClass | not installed; the rung for VMs without nested virtualization; file I/O penalty on clone-heavy Runs |
| A5 | Kata micro-VM | KVM, so bare metal or nested virtualization; the `kata-containers` extension | **today**: bronze, copper; 160Mi and 100m per pod |
| A6 | Kata on a confidential VM (SEV-SNP), or a dedicated node or cell per tenant | hardware; the `kata-containers-snp` extension from Talos 1.14 | not available |

#### B. Credential exposure: what the agent process can read

| Rung | Shape | Status |
| --- | --- | --- |
| B0 | real model key and forge token in the harness environment | what the chart renders for any `dind: true` workload and for a harness not yet qualified |
| B1 | in-worker loopback proxies; the harness gets placeholders | **today**; ADR-0034, valid only for single-tenant Runs without DinD |
| B2 | B1 plus placeholder checks, a branch fence and refusal under `CAP_SYS_PTRACE` | merged on `development` (#63, #64 and the VIK-1475 landing) and in the rc train the homelab runs; **today** once one qualification Run exercises the checks |
| B3 | credentials never enter the Run's pod: an egress gateway outside the pod swaps placeholders | designed in the 2026-09-28 plan, section 4; unbuilt |
| B4 | B3 with per-tenant forge Apps or vault paths, per-tenant audit and revocation | [ADR-0016](../adrs/0016-forge-registry-and-per-run-repo-scoped-credentials.md), proposed |

#### C. Network egress

| Rung | Shape | Status |
| --- | --- | --- |
| C0 | open | the chart renders no policy for KEDA and CronJob pods |
| C1 | namespace-shaped allowlist: own namespace, model gateway, forge, tracker | **today** |
| C2 | workload-shaped: a worker reaches DNS, ploegd, the model gateway and the forge, nothing else | the sandbox template's `Managed` policy can do this; plan step H4 and G8 |
| C3 | C2 plus a logged allowlist proxy for registries, opted in per team | ADR-0037 accepted, not rendered |
| C4 | the Run has one network peer, the gateway of B3 | with B3 |

#### D. Authority in the forge

| Rung | Shape | Status |
| --- | --- | --- |
| D0 | one long-lived bot token for every Run | the chart's shape without `botPasswordSecret` |
| D1 | read-only for readers; per-Run repo-scoped token for writers, revoked at settle | **today** |
| D2 | D1 plus a branch fence and a route allowlist at the proxy | VIK-1475 |
| D3 | D2 plus server-side protection on every target: PR-only, required checks, the per-repo delivery contract | homelab ADR-0050 covers the homelab; other targets vary |

Authority in Kubernetes is already at the top of its axis: workers hold no token, the launcher
holds one namespaced verb set. Keep it there.

**E. Spend** is at its top rung: authorize-then-settle per Run, pool per Shift, cap per Role.
ADR-0032 names it the primary differentiator. The ladder exists to protect it, because every
rung below is a way an agent could spend or push outside what was authorized.

#### F. Tenancy and blast radius

| Rung | Unit | Status |
| --- | --- | --- |
| F0 | one namespace, shared nodes | **today**; the domain model already names one default self-hosted Tenant |
| F1 | a namespace per Client or Team | chart renders one release per namespace, so possible by installing twice |
| F2 | a node pool per Tenant, by label and taint | the chart has `nodeSelector` but no tolerations knob |
| F3 | a cluster per Tenant or trust zone | section 5; Tenant and Client are the domain's own terms (`docs/domain/model.yaml`), proposed |

**Where the homelab stands:** bronze and copper at (A5, B1, C1, D1, E top, F0); silver and
pewter at (A1, B1, C1, D1, E top, F0). The weakest configuration the chart will still render
without complaint is (A0, B0, C0, D0). The strongest configuration this design reaches is
(A5 or A6, B3, C4, D3, E, F3).

**The two rules for sliding.** Moving up a rung on A is a node capability question (does the
cell have KVM, gVisor, id mapping) and an image question (does the harness image run as
non-root). Moving up on B, C and D is a product question: the chart and ploegd have to
render and enforce it. Moving down is always a values change, which is exactly the problem.
A posture should be declared once, validated at render, labelled on the pod and enforced at
admission, so that moving down is a visible diff that a policy can refuse.

## 4. Isolation classes: one declaration instead of six knobs

Bundle the axes into named classes a team or Role declares and a cell advertises:

| Class | A | B | C | D | Where it runs |
| --- | --- | --- | --- | --- | --- |
| `standard` | A3 (restricted plus user namespaces) | B2 | C2 | D2 | any conformant cluster, including cloud VMs without nested virtualization |
| `hardened` | A4 (gVisor) | B3 | C4 | D2 | any cluster with the gVisor runtime; pays the I/O penalty |
| `isolated` | A5 (Kata) | B3 | C4 | D2 | bare metal or nested-virtualization nodes |
| `confidential` | A6 | B3 | C4 | D3 | future hardware; defined now so the ladder has a top |

Anything with `dind: true`, `keyIsolation: ""` or `forgeTokenIsolation: ""` is `legacy` and
renders only with an explicit `allowLegacy: true`. The chart validates each class's
prerequisites at render (a `runtimeClassName` for `isolated`, a non-root image for
`standard`), labels every Run pod `ploeg.webgrip.dev/isolation-class`, and an estate's
admission policy enforces the label's meaning the way gap G4 proposed.

**The default is the strictest class the cell can honour.** A team or Role declares a floor,
not a target. `standard` is the floor when nothing is declared. A cell runs every Run it
accepts at the highest class it advertises, so a Kata cell runs `standard` work under Kata
too, because the overhead is small and the decision then needs no human. The hub never offers
a Run to a cell below the Run's floor, and a Run that no cell can honour waits and is reported,
it is never downgraded. Degradation is a written exception: a team sets `maxIsolationClass`
with a reason, for example gVisor's file I/O cost on a clone-heavy repository, and the
exception shows on the Run's label. A Role may raise its floor above its team's but never
lower it. On the axes that need no hardware, B, C and D, the chart defaults to the top rung
it can render, so the product supports every rung and ships the strict one.

Both writing and reading Roles execute repository code, so they take the same class. The
difference between them is authority and budget, which the D and E axes already express.

The homelab mapping on the day this ships: the home cell advertises `isolated` on its three
Kata workers, so all four teams run `isolated` there once B3 and C2 land, with the floor at
`standard`. silver and pewter stop being the weaker shape not because their values change but
because the cell decides. The `agent-runner` image still needs a `USER` and the chart the
worker `securityContext` and `hostUsers` knobs before any cell can honour `standard` at all.

## 5. Hub and cells: the architecture for scale

### The idea

Separate authority from capacity. One **hub** holds everything that is money, identity,
state or policy. Any number of **cells** hold capacity. A cell is a Kubernetes cluster that
runs a thin cell agent, the sandbox substrate and an egress gateway. Cells **pull** work and
the hub never dials in, so a cell can sit behind NAT, in any cloud, in a customer's own
cluster or on a laptop with k3s. This is the shape every CI runner already has, and the
executor contract already permits it: "anything speaking the run API qualifies", and an
executor without database credentials polls by spawning.

### Which dependency goes where

| Role | Components | Count | Holds |
| --- | --- | --- | --- |
| Hub | ploegd, PostgreSQL, the signing key, the bootstrap registry, the LiteLLM admin authority, Unfold | one, highly available | all durable state and all minting authority |
| Shared services | tracker, forge, model gateway, image registry | one each, reachable over TLS by every cell | the data planes Runs read and write |
| Cell | cell agent, agent-sandbox controller, RuntimeClasses, node pools, a local StorageClass for workspaces, a registry pull-through cache, the egress gateway | many, stateless, disposable | nothing durable; at most per-Run credentials in its gateway |

Today the homelab is hub and cell in one cluster, with in-cluster URLs. The first remote cell
makes the shared services' public TLS hostnames the only URLs a cell knows.

### Credentials in a cell: two modes

The 2026-09-28 plan already concluded that credentials must leave the Run's pod for a
multi-tenant or Kata-walled Run. A cell gives that gateway a home and a choice.

| Mode | Where the placeholder is swapped | What the cell holds | Traffic path | Use when |
| --- | --- | --- | --- | --- |
| cell gateway | in the cell, by its gateway, with per-Run credentials the hub mints at claim and revokes at settle | one short-lived, repo-scoped, budget-capped credential per running Run, outside every pod | Run to cell gateway to forge or model gateway | the cell's operator is trusted with its own tenants' Runs: your own clusters, a customer's own cluster |
| zero-secret cell | in the hub, by a hub gateway; the cell gateway only funnels | nothing | Run to cell gateway to hub gateway to forge or model gateway | the capacity is trusted less than the credentials: burst VMs rented by the hour, shared cells |

Both modes keep the Run at B3 and C4. The second doubles the hub's bandwidth for git and model
traffic, which matters for a hub on a residential uplink. Default to the first and keep the
second as a per-cell flag.

### Matching Runs to cells

The hub keeps a cell registry. A cell registers with an identity and a capability set:
isolation classes it can honour (`standard`, `hardened`, `isolated`), architecture, a
residency tag (for example `eu`), a cost class, and `maxRunning`. A team or Role declares its
class and optional placement constraints. The claim gains a cell dimension: the cell agent asks
for work it can honour, and ploegd returns only Runs whose class is at or below the cell's
maximum and whose constraints the cell meets. A Run never lands below its class. Backpressure
is the existing team cap plus the cell's own cap, both enforced in the claim transaction.

[ADR-0072](../adrs/0072-keda-stays-and-ploegd-reserves-a-run-for-each-launcher.md)
(proposed 2026-10-06) already gives this a shape: KEDA stays, ploegd serves
`GET /api/v1/executor/demand` over HTTP with a bearer token, and each launcher reserves one
specific Run with `POST /api/v1/runs/reserve` before it creates a sandbox, so KEDA needs no
database role and a full cluster is a wait, not a failure. A cell is that design pointed at a
hub URL: the demand route and the reservation carry the cell's identity and its advertised
classes, and ploegd answers only with Runs the cell may honour. ploegd still learns nothing
about Kubernetes, which [ADR-0071](../adrs/0071-ploeg-keeps-its-state-in-postgresql-and-is-not-a-kubernetes-operator.md)
and [ADR-0077](../adrs/0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md)
require: a class name and a residency tag are opaque strings to it. The cell agent's inner
loop is today's `ploeg-worker sandbox-launch`: one `SandboxClaim` per reservation, the same
backstops, the same run protocol.

### Identity and transport

A cell authenticates the way a worker does today: a per-cell bootstrap token exchanged for an
HMAC capability. The scope grows from (team, role) to (cell, team, role), and keys get
generations so one cell can rotate without the others. The second step is federation: the
cell's cluster issues projected ServiceAccount tokens, the hub verifies them against the cell's
OIDC discovery document, and no shared secret exists per cell. That is the mechanism ADR-0016
proposed for forge brokers, reused. Off-cluster transport is TLS, required: ploegd behind the
gateway with a certificate, the cell agent pinning it. mTLS over SPIFFE is a re-evaluation
trigger, not a prerequisite.

### What has to change in the product

Every coupling point below was read from the source on 2026-10-17.

1. ploegd serves plain HTTP (`cmd/ploegd/main.go`, `ListenAndServe`) and the default
   `apiUrl` is `http://<release>:<port>`. Off-cluster needs TLS, enforced by the cell agent
   refusing `http://` for a non-loopback hub.
2. The KEDA scaler still reads PostgreSQL with the `ploeg_scaler` credential on `development`.
   ADR-0072 replaces that with the HTTP demand route and a reservation per launcher; a cell
   needs those two routes to carry a cell identity and its classes, nothing more.
3. The sandbox launcher reads the in-cluster service account and creates claims in
   `POD_NAMESPACE`. It stays cell-local by design; the cell agent wraps it.
4. One Helm release renders ploegd and every executor from the same `executor.*` values. Split
   into `ploeg` (hub) and `ploeg-cell` (executor only), with `cell.hubUrl`, `cell.token`,
   `cell.capabilities` and the substrate values.
5. The bootstrap registry is scoped (team, role) with one signing key and no generations.
6. `LLM_BASE_URL` and the forge URL are single global values and the examples are
   `svc.cluster.local`. Cells need the public hostnames; ADR-0016's per-forge registry
   generalises this.
7. Network policy for KEDA and CronJob pods lives outside the chart. The cell chart renders
   the C2 policy and the gateway, so a cell is correct on a cluster that has no policy of its
   own.
8. Placement knows only `nodeSelector` and `runtimeClassName`. A cell needs tolerations for a
   tainted agent pool.

### What Unfold needs from this

Unfold is hub-side. Its interactive workspaces are Runs by another name and should take the
same classes and the same cells, so that the workspace egress stops being wider than the Run
egress. That convergence is Unfold's to decide and is noted here, not designed.

### What a cell looks like on the homelab estate

The homelab cluster becomes the hub plus cell `home`, advertising `isolated` on its three Kata
workers. A second cell on a dedicated Hetzner server advertises `isolated`; a cell on Hetzner
Cloud VMs advertises `standard` and `hardened` only, because those VMs are reported not to
offer nested virtualization. Talos cells can be managed by Omni or by the plain-talosctl
inventory; a cell can equally be k3s or a managed cloud cluster, because the daemonless design
needs no node prerequisite below `standard`.

### Where the hub and the cells live

The hub is the only part that must be reachable by every cell and must not lose state. Cells
are disposable. That asymmetry decides the hosting question: place the hub where its
availability and your control are both highest, and place cells wherever capacity is cheapest
for the class they advertise.

| Hub location | What it is | Cost | Availability | Control |
| --- | --- | --- | --- | --- |
| The homelab cluster, today | ploegd, Postgres, Unfold stay where the tracker, forge and model gateway already are; public TLS through the external gateway | nothing new | one residential uplink and one house; N-1 capacity is already tight | highest: no third party anywhere in the control path |
| An owned dedicated server in the EU | a Talos or k3s cluster on a Hetzner auction box, with Postgres and its WAL archive | on the order of €40 to €55 a month for an AX41-class box per a 2026 review; the auction varies daily | datacenter power and network; still one box until a second is added | high: the hardware is rented, the stack is yours |
| EU managed Kubernetes | Scaleway Kapsule, OVHcloud, STACKIT or IONOS; the control plane is free or cheap at most of them, nodes are billed | nodes from tens of euros a month; check live, prices moved in 2026 | provider SLA, multi-node by default | medium: a managed control plane is a third party in the ops path |
| A US hyperscaler | | | | not considered: cost, residency, and the owner's preference against it |

| Cell location | Classes it can advertise | When |
| --- | --- | --- |
| The homelab workers | `isolated` | now |
| A Hetzner dedicated server | `isolated` | the first remote cell, if Kata capacity is the goal |
| Hetzner Cloud VMs | `standard`, `hardened` | cheap burst capacity; Kata needs nested virtualization these VMs are reported not to offer |
| A Talos VM on garage-fsn1 | `isolated`, with nested virtualization enabled on that host | zero rent, shared with the backup target |
| A customer's or a second estate's cluster | whatever it advertises | the product story |

Staged recommendation:

1. **Now, while only this estate depends on it:** hub at home. Add the first remote cell on
   the machine the owner picks, and treat it as the qualification of P5 and P6.
2. **The day a second party depends on it:** move the hub to an owned EU dedicated server
   pair or to EU managed Kubernetes, with Postgres replicated and its WAL archived off the
   box. The shared services stop being "the homelab's Forgejo and Vikunja" and become
   per-tenant pointers through ADR-0016's registry, so the hub carries no tenant data plane.
   The trigger is a fact, not a feeling: a customer cell registers, or the home uplink causes
   a missed Run.
3. **Fleet management for Talos cells is optional, and cells must not depend on Sidero's
   hosted services.** Omni, hosted or self-hosted, can manage Talos cells and is free at this
   scale, but nothing inside a cell may require Omni to run. Sidero Labs changed owners on
   2026-09-14. Three facts about its services, checked 2026-10-17, shape the design:
   * Talos 1.14 stopped publishing the installer image to ghcr.io; the Image Factory is the
     only source. The homelab already installs from `factory.talos.dev`. The factory is
     MPL-2.0 and can be self-hosted; until then, pin installer digests and mirror the ones in
     use in the estate's own registry.
   * The KubeSpan discovery service is BUSL-1.1 with no additional use grant until
     2030-10-05, so a cluster stretched over KubeSpan depends on Sidero's hosted discovery
     service or a commercial licence. Cells need no KubeSpan: each is its own cluster and
     talks to the hub over TLS. That is a second reason to prefer cells over a stretched
     cluster.
   * A cell must survive a change in Omni's terms by re-provisioning with plain `talosctl`,
     the path ADR-0062 in homelab-cluster already keeps.

## 6. The path from here, each step independently shippable

Product, in order of dependency:

- **P1. Safe chart defaults.** `dind: false`, `keyIsolation: proxy`, `forgeTokenIsolation:
  proxy`, `workerAuth.mode: managed`; `legacy` renders only with `allowLegacy: true`.
  Confirmation: goldens, and a `ci/reject-legacy-without-opt-in-values.yaml` that fails.
- **P2. Worker pod shape knobs.** A worker `securityContext` (gap G2), `hostUsers`, and
  `runtimeClassName` for KEDA and CronJob pods (gap G7). Confirmation: a golden with
  `hostUsers: false` and `runtimeClassName: gvisor` on one Role.
- **P3. Isolation classes.** The `isolationClass` value per team and Role, render-time
  prerequisite checks, the pod label. Confirmation: a render of `isolated` without a
  `runtimeClassName` fails; every golden pod carries the label.
- **P4. Prove B2 live.** The three fixes are merged. One qualification Run per qualified
  harness with the placeholder check on, added as rows to the 2026-10-04 qualification record.
- **P5. Cells.** The `ploeg-cell` chart, the cell registry, (cell, team, role) scopes with key
  generations, ADR-0072's demand and reserve routes carrying a cell identity and its classes,
  class-aware reservations, TLS required off-cluster.
  Confirmation: a cell on a second cluster claims and completes one Run of each class it
  advertises, and a Run declared above the cell's class is never offered to it.
- **P6. The egress gateway.** Both modes. Confirmation: a canary Work Item that writes `env`,
  `/proc/1/environ` and `git config -l` into its pull request shows placeholders only, from
  a cell.
- **P7. Per-forge registry (ADR-0016).** Multi-forge cells and per-tenant credentials.

Estate, alongside:

- **E1.** Give `agent-runner` a `USER`; then raise `ploeg` to PSA `restricted` and delete the
  drop-all-capabilities waiver. Confirmation: `kubectl get ns ploeg` shows `enforce: restricted`
  and a Run completes.
- **E2.** Split `ploeg-allow-egress` per workload so workers cannot reach `ploeg-db:5432`;
  name the hosts Unfold workspaces may reach instead of the whole gateway namespace.
  Confirmation: the worker egress probe gains a `ploeg-db:5432 refused` assertion.
- **E3.** Add the gVisor extension and RuntimeClass for VM cells. Confirmation: the kata smoke
  Job gets a gVisor twin.
- **E4.** The first remote cell, on the box the owner picks. Confirmation: P5's.

## 7. Decisions this asks of the owner

1. **The tenancy unit is the cell for trust, the namespace for teams inside a cell, and the
   node pool for runtime capability.** Recommended as stated.
2. **Every cell runs every Run at the strictest class it can honour, and `standard` is the
   floor** below which a Run waits rather than degrades. A team lowers its ceiling only with
   a written reason. This is the owner's steer of 2026-10-17, "support many ways, default to
   the most stringent", made mechanical. Recommended.
3. **Credentials default to the cell-gateway mode**, with the zero-secret mode per cell.
   Recommended.
4. **Chart split into hub and cell**, and the product's defaults become the safe shape.
   Recommended, and P1 should ship before anything else because it costs least and closes the
   gap a new user falls into on day one.
5. Whether Unfold workspaces join the cells in this programme or a later one.
6. **Where the hub lives.** Home now, and an owned EU dedicated server or EU managed
   Kubernetes the day a second party depends on it, per the staged recommendation above.
   Recommended. The first remote cell's machine is a separate, smaller choice.

## 8. Re-evaluation triggers

- kubernetes-sigs/agent-sandbox ships per-sandbox credential injection, warm pools fit for
  cold Runs, or any multi-cluster construct: consume it and shrink the cell agent.
- Talos ships the `kata-containers-snp` extension on hardware the estate has: define
  `confidential` for real.
- Hetzner Cloud offers nested virtualization: `isolated` on cloud VMs.
- Cilium `toFQDNs` is adopted in an estate: an alternative to the Squid proxy of ADR-0037 at C3.
- A second estate or a customer runs a cell: the cell-identity federation step moves from
  "later" to "now", and the hub leaves the homelab.
- Sidero Labs, owned by Yardi since 2026-09-14, changes Omni's terms, the Image Factory, or
  how Talos installer images are published: re-check that every cell can be re-provisioned
  without Omni, and that the Kata and gVisor extensions still ship.

## More information

- Inventories this dossier was built from: the homelab layers (ADR-0053, the Kyverno
  policies, the `ploeg` and `unfold-workspaces` namespaces, the probes and alerts) and the Ploeg
  chart and executor (`ops/helm/ploeg/values.yaml`, `_helpers.tpl`, `_sandbox.tpl`,
  `pkg/sandboxlaunch`, `pkg/httpapi/worker_auth.go`, `cmd/ploegd/main.go`).
- [2026-09-28-credential-isolation-cluster-plan.md](2026-09-28-credential-isolation-cluster-plan.md),
  whose steps G1 to G8 and H1 to H7 this dossier reorders under the class model. Of those, H1 to
  H3 are live on the homelab and H4 is partly live.
- [docs/contracts/executor.md](../contracts/executor.md), [worker-control.md](../contracts/worker-control.md).
- Landed since the first read and folded in above:
  [2026-10-04-isolation-qualification.md](2026-10-04-isolation-qualification.md) (the proxy
  defaults and the qualified-harness set),
  [2026-10-05-ploeg-boundary-audit.md](2026-10-05-ploeg-boundary-audit.md),
  [ADR-0071](../adrs/0071-ploeg-keeps-its-state-in-postgresql-and-is-not-a-kubernetes-operator.md),
  [ADR-0072](../adrs/0072-keda-stays-and-ploegd-reserves-a-run-for-each-launcher.md),
  [ADR-0077](../adrs/0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md) and
  [ADR-0078](../adrs/0078-runs-reach-read-only-mcp-tools-only-through-the-gateway-bounded-by-a-litellm-team.md),
  which puts MCP access on the C axis's single-peer rung.
- Backlog #74 (RuntimeClass per team) and #127 (qualify the sandbox executor against a real
  controller) are absorbed by P2, P3 and P5.
