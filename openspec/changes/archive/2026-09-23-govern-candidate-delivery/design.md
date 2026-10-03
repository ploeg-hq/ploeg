# Context

[ADR 0024](../../../docs/adrs/0024-operator-work-uses-one-execution-authority.md) supplies operator execution authority. [ADR 0025](../../../docs/adrs/0025-management-authority-stays-in-the-control-plane.md) separates management credentials. R16/R17 require delivery authority to remain outside the worker. Existing worker PR discovery and Shift comment publishing are legacy behavior and are not evidence of trusted verification.

# Goals and Non-goals

Provide a usable control-service pipeline from immutable evidence through approval to a single reserved publication effect. Preserve actor, consumer and Team scoping. Defer successor attempts, automated merging, arbitrary repositories and policy execution inside Ploeg.

# Decisions

Use append-only tables for a Delivery Candidate, a Verification Receipt and an approval; use one mutable Publication Operation state guarded by an execution row lock. Server-selected repository policy pins verifier identity, policy digest and minimum tests. Separate `verify` consumer capability gates receipt and publication-effect reporting. HTTP callers cannot manufacture this capability through request fields.

A first publication reservation commits `reserved` before returning `effectAuthorized=true`. Replays return the operation with `effectAuthorized=false`; there is no lease that regenerates authority. The only progress after ambiguity is a positive exact-identity report. The trusted control service must preserve the operation identifier in remote metadata and validate evidence before reporting it.

# Risks and Trade-offs

Losing the first reservation response sacrifices automatic liveness to prevent duplicate publication. A configured verifier consumer is a trusted service boundary; it must not share credentials with a worker. Ploeg validates attested bindings and policy but does not execute checks or independently contact the forge in this slice. Existing registered execution uniqueness excludes a successor attempt, so this lifecycle is frozen after candidate creation; the contract exposes that limit. No queue or KEDA predicate changes are required.

# Migration

Migration 0015 adds empty repository-registration columns for existing executions. New admissions populate them. Old executions lacking a complete registration cannot admit candidates. New tables do not modify historical Outcome or PR records. Empty policy configuration and `publicationEnabled=false` fail closed.

# Open Questions

Human ratification of [ADR 0027](../../../docs/adrs/0027-candidate-delivery-uses-trusted-evidence-and-a-publication-barrier.md) and production qualification of the independent verifier and forge adapter remain explicit rollout steps.
