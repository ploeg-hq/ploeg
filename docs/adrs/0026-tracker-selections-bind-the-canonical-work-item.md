---
status: proposed
date: 2026-09-11
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2026-10-31
---

# Tracker selections bind the canonical Work Item

## Context and Problem Statement

Operator sessions and unattended dispatch can both originate from a tracker selection. A second manual Work Item for the same task defeats the shared authority established by ADR 0024 and permits duplicate paid work.

## Decision Drivers

* Preserve tracker identity and authority over content.
* Enforce R1, R2, R7, R11, R12 and R13 at the existing database locks.
* Reject uncertain or already-started work before creating inference authority.

## Considered Options

* Bind the canonical pristine Work Item under a durable operator ownership fence
* Create a manual duplicate carrying a tracker link
* Transfer arbitrary live unattended Runs into the workbench

## Decision Outcome

Chosen option: "Bind the canonical pristine Work Item under a durable operator ownership fence", because the existing queue identity and locks must arbitrate every path into execution. The tracker provider, external ID and origin remain unchanged. Operator ownership is represented by its execution binding and survives stop, expiry, completion and tracker webhook replay until a future explicit relinquishment policy is implemented.

Admission verifies the registered singleton Tracker Provider API root, Scope, fresh explicit open state and native revision, then locks and compares the Work Item's expected revision, timestamp, Team and exact Work Target. Lookup and new admission also compare current configured routing and container Team pins; changed or unavailable routing fails closed before another webhook refreshes the mirror. Only queued work without prior execution, durable checkpoints or accounting effects is eligible. Unstarted pending Runs may be retired atomically; running or historical work requires a later transfer design. Missing mirrors are never created by this entry point.

### Consequences

* Good, because independent callers cannot acquire two execution authorities for one task.
* Good, because tracker content remains identifiable and updates do not silently change the execution target.
* Bad, because existing operator ownership is deliberately sticky and requires a future explicit release policy for subsequent unattended work.
* Bad, because source freshness uses separate provider and database checks rather than an unavailable distributed transaction.

### Confirmation

Run `mise exec -- go test ./pkg/httpapi ./pkg/store ./pkg/shiftengine ./pkg/provider/... ./internal/ledger` and the cross-service qualification. Verify concurrent claim/admission, pending-Shift adoption, source-root/scope/native-revision/target mismatches, lost-response replay, no duplicate Work Item or paid effects, and webhook refresh after cancellation. Provider tests must distinguish unknown and closed status from explicit open state.

## Pros and Cons of the Options

### Manual duplicate

* Good, because it reuses the existing manual admission body.
* Bad, because tracker deduplication and leases no longer describe the same work.

### Arbitrary live transfer

* Good, because a person could intervene in already-running work.
* Bad, because repository state, paid capabilities and stop confirmation need a separately qualified transfer protocol.

## Re-evaluation triggers

* One provider kind must represent more than one configured tracker instance.
* A user needs to adopt a started Run or resume a previous branch.
* A reviewed operator candidate needs explicit return to unattended dispatch.

## More Information

* Technical story: [binding design](../../openspec/changes/archive/2026-09-23-bind-tracker-execution/design.md).
* Related decisions: [0010](0010-shift-owns-the-item-lease-owns-the-branch.md), [0014](0014-work-target-is-a-work-item-attribute.md), [0024](0024-operator-work-uses-one-execution-authority.md).
* 2026-09-11 — Recorded before implementation under the owner's instruction to continue the unified baseline. Human ratification remains pending.
