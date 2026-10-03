# Why

Operator executions produce repository evidence, but a worker assertion cannot authorize a forge write. Ploeg needs a durable binding between the completed Run, a canonical Delivery Candidate, independently obtained Verification Receipt and an explicit human approval, following R16 and R17.

# What Changes

Add Store authority exposed through the authenticated operator API: immutable Delivery Candidates, configured verification policies, trusted-control Verification Receipts, candidate-bound approvals, and durable Publication Operations. Publication starts disabled. An uncertain external result freezes its existing operation.

# Capabilities

## New Capabilities

- `operator-delivery`: govern candidate delivery for completed operator executions.

# Impact

The Store seam gains append-only migration 0015 and an additive operator API contract. De Vloer supplies canonicalization, independently executed checks and isolated forge effects through its control service. Existing worker Outcome reports acquire no verification authority. The bounded slice advances the operator intervention and reviewed evidence work in [the backlog](../../../docs/backlog.md).

# Non-goals

No new tracker, unattended publisher, merge authority, arbitrary policy uploaded by workers, or replacement Harness. No successor delivery attempt or general Work Order migration. Existing execution uniqueness freezes one delivery lifecycle; failed verification requires human follow-up outside this attempt. The tracker remains authoritative under the concurrent tracker-binding change.
