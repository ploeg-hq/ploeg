---
status: proposed
date: 2026-10-17
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-01
---

# Execution runs in cells that pull from one hub

## Context and Problem Statement

Ploeg assumes one cluster. ploegd, PostgreSQL, KEDA, the launchers and the worker pods
share a namespace, plain HTTP, in-cluster service names and the cluster's own network
policy. Read against `development` on 2026-10-17, eight couplings hold that assumption in
place: ploegd serves plain HTTP; KEDA reads PostgreSQL with its own role; the sandbox
launcher uses the in-cluster service account and creates claims in its own namespace; one
Helm release renders ploegd and every executor from the same values; the bootstrap
registry is scoped (team, role) with one signing key; `LLM_BASE_URL` and the forge URL are
single values with `svc.cluster.local` examples; network policy for worker pods lives
outside the chart; placement knows `nodeSelector` and nothing else.

The owner wants Ploeg to run agents across clusters, and wants that to be a selling point.
The owner also decided on 2026-10-03 that Unfold's production runs on a rented EU Talos
cluster with the homelab as staging ([VIK-1764](https://vikunja.webgrip.dev/tasks/1764)),
and that Runs under `isolated` need Kata, which needs KVM, which rented cloud VMs are
reported not to provide. So production and the strongest execution are on different
machines by construction.

Where does execution run, and what may a cluster that runs it know and hold?

## Decision Drivers

* **ploegd holds no Kubernetes rights and knows no vendors**
  ([ADR-0072](0072-keda-stays-and-ploegd-reserves-a-run-for-each-launcher.md),
  [ADR-0077](0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md)), and
  keeps its state in PostgreSQL
  ([ADR-0071](0071-ploeg-keeps-its-state-in-postgresql-and-is-not-a-kubernetes-operator.md)).
* **Capacity pulls, authority never dials in.** A cluster behind NAT, in any cloud or on a
  customer's premises must be able to execute without an inbound port.
* **A Run's credentials never enter a cluster that is trusted less than they are.**
* **Any conformant cluster qualifies.** No KubeSpan, no node prerequisite below the
  `standard` class of
  [ADR-0106](0106-a-run-declares-an-isolation-floor-and-a-cell-runs-it-at-the-strictest-class-it-can-honour.md).
* **The Tenant boundary** the domain names and
  [VIK-1741](https://vikunja.webgrip.dev/tasks/1741) asks for must be expressible as
  infrastructure, not only as rows.

## Considered Options

* A hub and cells: one control plane, many pulling execution clusters
* One cluster stretched across sites with KubeSpan
* One ploegd per cluster, federated
* Stay single-cluster and size that cluster

## Decision Outcome

Chosen option: "**a hub and cells**", because it is the executor contract generalised
rather than a new system: workers already claim over HTTP, ploegd already needs no
Kubernetes API, and ADR-0072 already moves the scale signal to HTTP and the launch
decision to a reservation. A cell is that design pointed at a hub URL.

* **Hub.** ploegd, PostgreSQL, the signing key, the bootstrap registry, the LiteLLM admin
  authority and Unfold. One, highly available, the only place with durable state or
  minting authority.
* **Shared services.** Tracker, forge, model gateway and image registry stay where they
  are and become TLS hostnames every cell can reach.
* **Cell.** A Kubernetes cluster running the cell agent, the agent-sandbox controller, its
  RuntimeClasses and node pools, a local StorageClass for workspaces, a registry
  pull-through cache and an egress gateway. It holds nothing durable. It is installed from
  a `ploeg-cell` chart, separate from the hub chart.
* **Registration.** A cell registers with an identity and advertises capabilities as
  opaque strings: the isolation classes it can honour, an architecture, a residency tag, a
  cost class and `maxRunning`. ploegd compares strings; it learns nothing about
  Kubernetes.
* **Demand and reservation.** ADR-0072's `GET /api/v1/executor/demand` and
  `POST /api/v1/runs/reserve` carry the cell's identity. ploegd answers only with Runs
  whose floor the cell can honour and whose placement constraints the cell meets. The
  team cap and the cell cap are both enforced in the claim transaction.
* **Identity and transport.** A cell authenticates as a worker does today, with a per-cell
  bootstrap token exchanged for an HMAC capability, scoped (cell, team, role), with key
  generations so one cell rotates alone. Federation through the cell cluster's projected
  ServiceAccount tokens and OIDC discovery is the second step. Off-cluster transport is
  TLS, required: the cell agent refuses an `http://` hub that is not loopback.
* **Credentials, two modes per cell.** In *cell gateway* mode the hub mints per-Run
  credentials at claim and revokes them at settle, and the cell's egress gateway holds
  them outside every pod. In *zero-secret* mode the cell gateway only funnels, and a hub
  gateway swaps placeholders, so the cell holds nothing. Both keep the Run itself at the
  `hardened` rung or above. Cell gateway is the default; zero-secret is for capacity
  trusted less than the credentials.
* **Placement of the first hub and cells.** The hub is the production cluster of
  VIK-1764. That cluster pairs rented control-plane and app VMs with one rented
  bare-metal worker joined over a private VLAN, so it honours `isolated` itself (owner
  decision on VIK-1767, 2026-10-18). The homelab is the staging hub and a second
  `isolated` cell. Shared-CPU cloud VMs become `standard` and `hardened` cells. Fleet
  management of Talos cells is optional and never a cell's dependency, per the owner's
  decision on VIK-1766.

### Consequences

* Good, because capacity is added by installing one chart on any cluster and pointing it
  at the hub, which is the selling point stated in product terms.
* Good, because the strongest execution, Kata on bare metal, and the cheapest
  availability, a rented control plane, no longer have to be the same kind of machine,
  and a cluster may mix both.
* Good, because a Tenant can be a cell: its Runs never share a kernel, a node or a network
  with another Tenant's.
* Good, because nothing in a cell depends on KubeSpan or on Sidero's hosted discovery
  service, which is BUSL-1.1 with no free-use grant.
* Bad, because git clones, image pulls and model streams cross the network between cells
  and the shared services. A per-cell pull-through cache and shallow clones bound two of
  the three; the hub on a residential uplink is the case to measure first.
* Bad, because the hub becomes a single point of failure for every cell. It already is
  for the one cluster; the cell design makes its availability a stated requirement.
* Bad, because the chart splits in two and the release train has to keep hub and cell
  compatible across versions, the problem
  [ADR-0064](0064-development-is-trunk-and-release-candidates-come-from-it.md) and the
  Unfold compatibility contract already solve for ploegd and its consumers.

### Confirmation

* `scripts/helm-golden.sh check` renders the `ploeg-cell` chart with no database
  credential, no LiteLLM master key and no signing key in any object, and refuses a
  non-loopback `http://` hub URL.
* `go test ./pkg/httpapi/` pins that demand and reservation answer only Runs whose floor
  the calling cell's advertised classes satisfy, and that two cells with different
  classes see different demand for the same team.
* A qualification record in `docs/research/` cites one Run per advertised class completed
  by a cell on a second cluster, and one Run declared above that cell's maximum that the
  cell was never offered.
* `ploeg-cell`'s egress gateway goldens show the worker pod with exactly one egress peer
  beyond DNS.

## Pros and Cons of the Options

### One cluster stretched across sites with KubeSpan

* Good, because nothing in Ploeg changes; the cluster grows.
* Bad, because etcd and the control plane cross the WAN, and the discovery service that
  makes KubeSpan traverse NAT is BUSL-1.1 with no free-use grant until 2030-10-05.
* Bad, because Longhorn, Cilium and the placement labels of one estate become every
  site's problem.

### One ploegd per cluster, federated

* Good, because each cluster is self-contained.
* Bad, because budgets, leases and the tracker webhook split across databases, and Unfold
  knows one Ploeg URL. The authorized-spend property of
  [ADR-0032](0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md) would have
  to be reconciled across hubs.

### Stay single-cluster and size that cluster

* Good, because it is the least work.
* Bad, because the owner's production decision and the Kata hardware already live on
  different machines, and "agents at scale" is then a claim with no mechanism.

## Re-evaluation triggers

* kubernetes-sigs/agent-sandbox ships a multi-cluster construct or per-sandbox credential
  injection: consume it and shrink the cell agent and the gateway.
* A rented cloud VM type offers nested virtualization: `isolated` on rented capacity.
* A second estate or a customer runs a cell: federated cell identity moves from the second
  step to the first.
* Sidero Labs changes the Image Factory or Talos release publishing: re-check that a Talos
  cell can be re-provisioned without any hosted Sidero service.

## More Information

* Evidence trail:
  [docs/research/2026-10-17-isolation-ladder-and-execution-cells.md](../research/2026-10-17-isolation-ladder-and-execution-cells.md),
  section 5, and
  [2026-09-28-credential-isolation-cluster-plan.md](../research/2026-09-28-credential-isolation-cluster-plan.md),
  section 4, whose per-tenant egress gateway this record gives a home.
* Related: [0072](0072-keda-stays-and-ploegd-reserves-a-run-for-each-launcher.md),
  [0071](0071-ploeg-keeps-its-state-in-postgresql-and-is-not-a-kubernetes-operator.md),
  [0077](0077-ploeg-knows-work-sources-and-change-destinations-never-vendors.md),
  [0016](0016-forge-registry-and-per-run-repo-scoped-credentials.md) (per-forge registry,
  which multi-forge cells need), [0106](0106-a-run-declares-an-isolation-floor-and-a-cell-runs-it-at-the-strictest-class-it-can-honour.md).
* Board: [VIK-1764](https://vikunja.webgrip.dev/tasks/1764) (the production cluster),
  [VIK-1741](https://vikunja.webgrip.dev/tasks/1741) (the Tenant boundary).
* 2026-10-18 — Placement amended: the production cluster carries its own bare-metal
  Kata worker (Hetzner AX41 over vSwitch, cap €185 a month), decided on
  [VIK-1767](https://vikunja.webgrip.dev/tasks/1767). The homelab is no longer
  production's only `isolated` capacity.
* 2026-10-24 — [ADR-0106](0106-a-run-declares-an-isolation-floor-and-a-cell-runs-it-at-the-strictest-class-it-can-honour.md) amended: a cell advertises the set of isolation classes it can honour, not a maximum, and `hardened` and `isolated` are alternatives. Read "declared above that cell's maximum" in the Confirmation as "declared at a class the cell does not advertise".
* 2026-10-25 — Amended by [ADR-0156](0156-each-run-carries-a-transaction-token-that-names-its-person-and-a-team-without-a-sponsor-runs-nothing.md): a cell's bootstrap token is exchanged for the Run's ES256 transaction token, not an HMAC capability; the HMAC capability stays issued for one minor and is deprecated. Federation through projected ServiceAccount tokens is still the second step.
