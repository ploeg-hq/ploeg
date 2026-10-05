---
status: proposed
date: 2026-10-05
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-04-01
---

# Ploeg keeps its state in PostgreSQL and is not a Kubernetes operator

## Context and Problem Statement

Ploeg runs on Kubernetes, and the docs call `ploegd` a controller. It has no CRDs, no `client-go` and no reconcile loop over Kubernetes objects. The owner asked whether Ploeg should become a Kubernetes operator, with Work Items, Shifts and Runs as custom resources reconciled by a controller.

Should Ploeg's execution state move into the Kubernetes API, with ploegd as an operator?

## Decision Drivers

* **Money is a ledger.** Budgets are authorized and settled ([ADR-0012](0012-two-level-budgets-authorized-and-settled.md)). Team caps are enforced under an advisory lock inside the claim transaction (`pkg/store/team_capacity.go`). Both need multi-row transactions.
* **Side effects must happen once.** Minting a key, pushing a branch and posting a finding must not repeat ([ADR-0060](0060-authenticated-webhooks-go-through-a-durable-inbox-and-required-publications-through-an-outbox.md)).
* **Durable state lives in Postgres and the forge.** This is R6 in [ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md).
* **Not every consumer runs on Kubernetes.** The delegated operator path runs in local, Docker or Kubernetes workspaces, and Ploeg names none of its consumers ([ADR-0069](0069-ploeg-names-none-of-its-consumers.md)).
* **Run cards need SQL.** They and their lists are relational queries over stored facts ([ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md), [ADR-0054](0054-a-card-list-finds-cards-by-roster-login-newest-activity-first.md)).

## Considered Options

* Keep execution state in PostgreSQL, and use Kubernetes only as the executor
* Make Ploeg an operator, with Work Items, Shifts and Runs as CRDs
* Keep state in PostgreSQL and add configuration CRDs (Team, Target, Forge) mirrored into it

## Decision Outcome

Chosen option: "**Keep execution state in PostgreSQL, and use Kubernetes only as the executor**". This is the only option that keeps the ledger transactional and makes side effects happen once.

An operator's reconcile loop is safe because it can run any number of times. Ploeg's core is the opposite: a ledger, plus effects that must not repeat. etcd has no transactions across objects, and a CRD `status` is not an append-only ledger. An operator would end up keeping an outbox, a lock and a ledger in CRDs: a weaker copy of the tables Ploeg already has.

The part of Ploeg that does fit the operator pattern is placing and isolating a pod. agent-sandbox is already that operator, and Ploeg is its client ([ADR-0072](0072-ploegd-launches-each-runs-sandbox-and-keda-leaves-the-sandbox-path.md)).

Configuration CRDs stay a later option. They would only mirror Team, Target and Forge registration into Postgres, and runtime state would never live in them.

### Consequences

* Good, because claims, caps, budgets and audit rows stay in one transactional store, with the tests that already cover them.
* Good, because the delegated path, and any future non-Kubernetes executor, keep working without a cluster.
* Good, because there is no new controller to run, upgrade or secure.
* Bad, because Ploeg configuration is not a native Kubernetes object. It reaches Git through chart values and `PLOEG_CONFIG`, not through `kubectl get teams`.
* Bad, because Kubernetes tools (`kubectl`, Flux health checks) cannot see Run state. Operators read it through the operator API, `/metrics` and the run cards.

### Confirmation

* **Code review.** A change that adds a CRD whose `status` carries Work Item, Shift, Run, Lease or budget state violates this record, and so does one that makes Kubernetes the source of truth for any of them. Rejection cites this ADR.
* **Dependency check.** `go list -m all` in CI shows no `sigs.k8s.io/controller-runtime`. A change that adds it must cite a record that supersedes this one.
* **State check.** `mise exec -- go test ./pkg/store/...` remains the suite that proves claim, cap and settlement semantics.

## Pros and Cons of the Options

### Make Ploeg an operator, with Work Items, Shifts and Runs as CRDs

* Good, because the cluster's own tools would show Run state, and Flux could check its health.
* Good, because the work would be declarative and live in Git.
* Bad, because etcd cannot hold a budget ledger or a capped claim transactionally.
* Bad, because a reconcile loop repeats effects that must happen once.
* Bad, because the delegated, local and Docker paths would need a cluster.
* Bad, because relational reads (cards, lists, grades) would need a second store anyway.

### Keep state in PostgreSQL and add configuration CRDs

* Good, because Teams and Targets would become reviewed manifests in a Flux-managed estate.
* Bad, because it adds a controller and a sync path for configuration that already reaches Git through values.
* Neutral: compatible with this decision, and can be adopted later without superseding it.

## Re-evaluation triggers

* Ploeg drops the delegated, local and Docker execution paths and becomes Kubernetes-only.
* agent-sandbox, or another SIG project, ships a dispatch layer with leases and refusing budgets that Ploeg could adopt (the [ADR-0032](0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md) trigger).
* An operator asks for Teams or Targets as manifests twice in a quarter. That reopens the configuration-CRD option only.

## More Information

* Evidence: [Substrate, language and Run bottlenecks](../research/2026-10-05-substrate-language-and-run-bottlenecks.md), §2.
* Related: [ADR-0005](0005-build-a-dedicated-dispatch-plane.md) and [ADR-0032](0032-keep-the-dispatch-plane-and-compete-on-authorized-spend.md) rejected Argo, Temporal and Conductor for the same reason: each would replace the store and the Leases.
* 2026-10-05: proposed after a five-spike review requested by the owner.
