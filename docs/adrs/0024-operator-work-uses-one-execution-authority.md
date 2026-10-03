---
status: proposed
date: 2026-09-10
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2026-10-31
---

# Operator work uses one execution authority

## Context and Problem Statement

The owner requested De Vloer as the human surface of Ploeg, with interactive and unattended work sharing execution. Two independent execution owners cannot safely coordinate intervention, budget admission or recovery.

## Decision Drivers

* Reuse the working Harness and Executor adapters.
* Preserve R1 and R2 across browser disconnects and lost command responses.
* Give humans a versioned API without exposing worker capabilities.

## Considered Options

* Ploeg admission and operator commands with De Vloer as workbench and delegated Executor
* Keep two execution owners and combine their displays
* Replace both services with another orchestration platform

## Decision Outcome

Chosen option: "Ploeg admission and operator commands with De Vloer as workbench and delegated Executor", because the current components can share authoritative execution identity without a rewrite. An authenticated read API is the first increment. Explicitly configured manual-origin Work Items extend the previous tracker-only admission restriction; tracker-originated work retains its original authority. Human interaction records may remain in De Vloer while Ploeg controls permission to execute.

Operator identities are bound to named consumers and allowed Teams. Read and execution permissions are separate. Commands carry idempotency identity and state preconditions; the same accepted command never creates two paid executions. Observation is independent of control transfer. Replacement execution remains blocked until prior effects and stop status are reconciled. Durable execution events serialize revisions per execution; existing audit sequence pagination is only a snapshot.

### Consequences

* Good, because a person can detach and return without changing the execution owner.
* Good, because existing adapters and interfaces remain useful.
* Bad, because another authenticated service boundary must handle partial failures explicitly.

### Confirmation

Run the repository Go build, vet and test gates plus the De Vloer cross-service tests. Verify scoped reads, duplicate command delivery, stale revision rejection, explicit stop after restart and no automatic paid resubmission after uncertain acceptance. Verify browser evidence against the real local services. Live provider and cluster qualification remain separate recorded gates.

## Pros and Cons of the Options

### Two execution owners with one display

* Good, because it requires fewer initial changes.
* Bad, because shared display does not prevent duplicate execution or conflicting intervention.

## Re-evaluation triggers

* A pinned alternative passes the actual tracker, gateway, intervention and recovery workflow with less operator effort.
* The delegated Executor cannot enforce the required admission and stop boundaries.

## More Information

* Technical story: [OpenSpec design](../../openspec/changes/archive/2026-09-23-unified-operator-execution/design.md).
* Evidence: [De Vloer unification research](https://forgejo.webgrip.dev/webgrip/de-vloer/src/branch/development/docs/research/2026-09-10-unified-workbench-baseline.md).
* 2026-09-10 — Recorded before implementation under the owner's instruction to implement the unified baseline. Status remains proposed pending explicit decision ratification.
* Related decisions: [0005](0005-build-a-dedicated-dispatch-plane.md), [0010](0010-shift-owns-the-item-lease-owns-the-branch.md).
