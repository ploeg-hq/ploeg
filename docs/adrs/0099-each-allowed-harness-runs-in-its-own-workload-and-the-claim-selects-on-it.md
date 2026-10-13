---
status: proposed
date: 2026-10-13
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Each allowed Harness runs in its own workload, and the claim selects on it

## Context and Problem Statement

When a Run can be given any Harness from its Team's allowlist ([ADR-0098](0098-an-operator-admission-may-choose-each-runs-harness-from-its-teams-allowlist.md)), a worker must end up with that Harness. Today each (Team, Role) workload runs one image with one Harness, built before the claim, with DinD and credential isolation decided per workload from the Harness. How does a requested Harness reach a worker?

## Decision Drivers

* The claim and the scale signal must use one predicate; undershoot stalls Work Items silently (`role-claim`).
* A misconfigured Harness should fail before a Lease is taken.
* Isolation and DinD depend on the Harness and are qualified per Harness.
* Ploeg publishes no agent image, and each worker pod runs one Run with one Harness.

## Considered Options

* One workload per (Team, Role, Harness); the claim and the scaler filter on the Run's stored Harness
* One multi-Harness runner image per (Team, Role); the claim response names the Harness and the worker builds the adapter after claiming

## Decision Outcome

Chosen option: "one workload per (Team, Role, Harness)", because it keeps every property a workload has today and changes the predicate by one column comparison.

* The chart renders one scaled workload for each allowed entry, with the entry's image, ACP settings, DinD and isolation.
* The scaler query, `ClaimRoleWithin` and `PendingRuns` add `harness_requested IS NULL OR harness_requested = <key>`, where the key is the chart's qualification name (`acp/<profile>` or the Harness name); a partial index serves it; the agreement test covers it. A demand endpoint replacing the SQL ([ADR-0072](0072-keda-stays-and-ploegd-reserves-a-run-for-each-launcher.md)) takes the Harness as a parameter.
* A Run with no stored Harness stays claimable by every workload of its Role. A Team without a plan keeps one workload, because its scaler counts queued Work Items, not pending Runs per Role.

### Consequences

* Good, because adapters are still built and checked before the claim, and isolation stays a per-workload, per-Harness decision.
* Good, because images stay small and Harness-specific.
* Bad, because workloads multiply by the allowlist length, and each Harness cold-starts on its own.
* Bad, because removing an allowlist entry while Runs are pending for it strands them; ploegd reports pending Runs per stored Harness and alerts on one outside the allowlist.

### Confirmation

* `TestClaimRoleAgreesWithPendingRuns` in `pkg/store` drains Runs across Harnesses and compares with depth.
* `mise run verify` renders a Role with two allowed Harnesses from an `ops/helm/ploeg/ci/` values file and compares it with the committed goldens (`scripts/helm-golden.sh check`): two workloads, each with its own query clause and environment, and a plan-less Team unchanged.

## Pros and Cons of the Options

### One multi-Harness image

* Good, because fewer workloads and warm pods serve every Harness.
* Bad, because the adapter is built after the claim, so a broken Harness takes a Lease first and charges a retry.
* Bad, because isolation and DinD would be decided per Run inside one pod whose privileges are fixed at start.
* Bad, because the image must carry every agent, and Ploeg publishes none.

## Re-evaluation triggers

* A Team's allowlist passes four entries, or KEDA polling of Ploeg's workloads shows in database load.
* Cold starts per Harness pass 60 s at p50 for a consumer's sessions.
* A Harness becomes qualified for isolation with DinD, removing the per-workload privilege difference.

## More Information

* OpenSpec change `select-harness-per-run`.
* 2026-10-13: the owner asked for a recommendation between one multi-Harness runner image and a worker pool per Harness; this record recommends the pool. Proposed.
* 2026-10-13 — The owner confirmed one workload per Harness (`select-harness-per-run` design Open Question 3, resolved). A model chosen per session ([ADR-0103](0103-an-operator-admission-may-choose-each-runs-model-from-the-models-its-roles-policy-lists.md)) travels in the claim response and adds no workloads.
