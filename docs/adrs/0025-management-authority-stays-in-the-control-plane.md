---
status: proposed
date: 2026-09-10
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2026-10-31
---

# Management authority stays in the control plane

## Context and Problem Statement

Worker images currently receive the LiteLLM management credential, and expired Runs can stop reserving budget before external charges are known. Filtering a child environment does not isolate a management credential held by a parent in the same workload.

## Decision Drivers

* Preserve ADR 0008 metering identity and ADR 0012 authorization semantics.
* Enforce R2 independently of worker cooperation.
* Give each Executor only its intended inference and control capability.

## Considered Options

* Server-side capability issuance with durable unresolved accounting
* Child environment filtering while retaining worker management credentials

## Decision Outcome

Chosen option: "Server-side capability issuance with durable unresolved accounting", because management authority must be absent from the workload itself. Ploeg mints, meters and blocks keys; workers receive Run-scoped inference capability through authenticated control requests. Environment construction is an allowlist. Blocking preserves alias and metering history. Worker outcomes report work results but do not prove trusted spend settlement. Unresolved accounts retain authorization after worker expiry and reconciliation is independent of worker liveness.

### Consequences

* Good, because a worker cannot mint unrelated inference credentials.
* Good, because a crash cannot make unknown paid work appear free.
* Bad, because unresolved external accounting may require operator reconciliation before further work is admitted.

### Confirmation

Run worker environment regression tests, broker HTTP authorization tests and PostgreSQL accounting tests. Assert rendered worker templates contain no management Secret references and crash-after-mint retains its budget hold. Run Go build/vet/test, Helm lint and every chart rendering from the existing CI workflow. Live gateway settlement is independently qualified.

## Pros and Cons of the Options

### Filter only child environment

* Good, because it is a small mitigation.
* Bad, because the parent workload still holds the administrative capability and shared process resources expose it.

## Re-evaluation triggers

* A gateway provides an authoritative final accounting watermark.
* Workload identity supplies short-lived broker authorization without bootstrap credentials.

## More Information

* Technical story: [OpenSpec worker authority spec](../../openspec/changes/archive/2026-09-23-unified-operator-execution/specs/worker-authority/spec.md).
* 2026-09-10 — Recorded before implementing worker isolation and accounting corrections; remains proposed for human ratification.
* Related decisions: [0008](0008-litellm-is-the-credential-and-metering-seam.md), [0012](0012-two-level-budgets-authorized-and-settled.md).
