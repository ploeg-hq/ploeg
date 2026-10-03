---
status: proposed
date: 2026-09-11
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2026-10-31
---

# Candidate delivery uses trusted evidence and a publication barrier

## Context and Problem Statement

A completed Run can provide code and test output without proving that the reviewed code passed independently executed checks. A forge timeout can leave an external write successful but locally unknown. Delivery authority must bind exact evidence and prevent a second effect after an ambiguous result.

## Decision Drivers

* R16 binds a Delivery Candidate to independently obtained Verification Receipt and human approval.
* R17 requires durable authority before publication and conservative recovery after uncertainty.
* Management capabilities remain outside the worker under [ADR 0025](0025-management-authority-stays-in-the-control-plane.md).

## Considered Options

* Store exact candidate evidence, trusted verification and a durable publication barrier in Ploeg.
* Accept worker reports and retry publication after an empty forge lookup.

## Decision Outcome

Chosen option: "Store exact candidate evidence, trusted verification and a durable publication barrier in Ploeg", because it makes both approval and crash recovery depend on durable control-plane facts.

The completed operator execution admits one immutable Delivery Candidate with canonical commit, tree, base, artifact and current policy digests. A separately configured verification-capable consumer submits a Verification Receipt that satisfies the server's registered repository policy. Human approval binds that receipt and candidate. Ploeg commits one Publication Operation before granting a single response permission to invoke the external effect. Replays never regenerate that permission. Only a trusted positive match of remote identity, branch and canonical commit can resolve an ambiguous operation as published.

The initial lifecycle deliberately freezes one attempt. It does not implement successor Work Orders or automatic repair after failed verification. Production publication remains disabled until an operator configures and qualifies the trusted verifier and isolated publisher.

### Consequences

* Good, because worker-authored success claims cannot become verification authority.
* Good, because timeout recovery cannot silently create a second pull request.
* Bad, because a lost reservation response can require human reconciliation even when no external effect occurred.
* Bad, because the trusted consumer remains an operational trust boundary; its credentials must stay outside agent workspaces.

### Confirmation

`mise exec -- go test ./pkg/store ./pkg/httpapi` checks immutable binding, scoped verification, policy drift, approval references, concurrent reservation, lost-response replay and positive-only reconciliation. `mise exec -- go test ./internal/ledger/` checks ledger consistency. The De Vloer fresh-container qualification must independently execute the checked canonical tree before enabling production publication.

## Pros and Cons of the Options

### Accept worker reports and retry an empty lookup

* Good, because existing workers already produce outcomes and discover pull requests.
* Bad, because self-reported verification and absence-based retry cannot establish R16 or R17.

## Re-evaluation triggers

* A durable Work Order and successor-attempt model is implemented.
* A forge offers an enforced idempotency key for create operations.
* Independent verifier identity is backed by workload attestation rather than a configured service capability.

## More Information

* Technical story: [govern-candidate-delivery](../../openspec/changes/archive/2026-09-23-govern-candidate-delivery/proposal.md).
* 2026-09-11 — Recorded before implementation; remains proposed for human ratification.
* Related decisions: [ADR 0024](0024-operator-work-uses-one-execution-authority.md), [ADR 0025](0025-management-authority-stays-in-the-control-plane.md).
