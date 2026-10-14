---
status: accepted
date: 2026-10-14
decision-makers: Ryan Grippeling
supersedes: 0024
review-by: 2027-01-31
---

# An Operator Consumer admits Work Items that ploeg-worker executes

## Context and Problem Statement

[ADR-0024](0024-operator-work-uses-one-execution-authority.md) (proposed) lets an Operator Consumer admit an Operator Execution and then execute it itself as a delegated Executor. Ploeg writes an operator-owned Work Item, Shift, Run and Lease for it that no worker claims, and the scheduler has to skip those rows. On 2026-10-13 the owner decided that `ploeg-worker` executes every session, that the Harness is chosen per session, and that interactivity is kept (Unfold ADR-0023 and ADR-0044). Ploeg has no way for a consumer to put work in a Team Queue. How does an Operator Consumer hand work to Ploeg so that Ploeg executes it?

## Decision Drivers

* One Executor for all work: dispatch, budget, Leases and recovery (R1, R2, R5) are the same for operator work and tracker work.
* A lost response, a restart or a vanished consumer never duplicates or restarts paid work (R3).
* The consumer stays outside the worker's network boundary and never holds a Run capability or a model key.
* Existing operator routes (withdraw, requeue under [ADR-0044](0044-an-operator-restarts-stopped-work-from-a-round-they-choose.md), notes under [ADR-0068](0068-context-added-while-a-shift-runs-reaches-the-next-run.md)) keep their meaning.

## Considered Options

* The consumer admits an ordinary manual-origin Work Item into its Team Queue; `ploeg-worker` executes it
* Keep Operator Executions and let `ploeg-worker` claim their Run
* The consumer creates a Tracker Item and Ploeg ingests it through the tracker

## Decision Outcome

Chosen option: "The consumer admits an ordinary manual-origin Work Item into its Team Queue", because it reuses Shift dispatch unchanged and removes the reason for placeholder rows.

* An Operator Consumer with `execute` permission and the Team in its scope admits a manual-origin Work Item through `POST /api/v1/operator/work-items`. The Work Item is not operator-owned. It is queued, its Shift opens from the Team's plan, and only `ploeg-worker` claims its Runs.
* The Admission is idempotent by a consumer-chosen request identity and a fingerprint of the request. A replay never queues the Work Item again; a changed replay is a conflict.
* The Work Target is a registered target key. The budget is capped by the consumer's `maxBudgetUsd` and caps the Shift pool.
* The consumer follows the work through the operator event feed and steers it with withdraw, requeue and notes. Pause, resume and messages to a running Run belong to attended Runs ([ADR-0100](0100-an-attended-run-relays-its-harnesss-requests-through-ploegd-and-falls-back-to-its-teams-policy.md), [ADR-0101](0101-a-paused-run-keeps-its-pod-and-session-for-a-bounded-time-and-is-never-retried-automatically.md)), the Harness choice belongs to [ADR-0098](0098-an-operator-admission-may-choose-each-runs-harness-from-its-teams-allowlist.md) and the model choice to [ADR-0103](0103-an-operator-admission-may-choose-each-runs-model-from-the-models-its-roles-policy-lists.md). The `harness`, `model`, `attended` and `autoApprove` fields are part of this Admission and of its fingerprint.
* Operator Executions, their placeholder rows and the scheduler exclusions are retired once no consumer admits them and none is open.

This supersedes ADR-0024 where it makes the consumer the delegated Executor and keeps execution state in an Operator Execution. Its named consumers with Team scope, separate read and execute permissions, idempotent commands, and observation independent of control stay.

### Consequences

* Good, because operator work and tracker work share one dispatch, budget path, review loop and recovery.
* Good, because the placeholder rows, about ten scheduler exclusions and a second liveness protocol can be deleted.
* Good, because the Harness choice and attended Runs have a single Admission to attach to.
* Bad, because admitted work counts against a Team's `maxRunning` and waits in the same queue as tracker work.
* Bad, because infrastructure failures are retried under the Team's recovery policy without a person asking.
* Bad, because two Admission paths coexist until consumers move.

### Confirmation

* `go test ./pkg/httpapi/ ./pkg/store/` covers authorization, the budget ceiling, replay without re-queue (also after `withdrawn` and `done`), the conflict on a changed replay, and a worker claiming the admitted Run.
* The operator contract test pins the Admission in `docs/contracts/operator-api.v1.schema.json` to its Go types.
* Review: no new code path creates an operator-owned Work Item or a Run with Role `operator` for this route.

## Pros and Cons of the Options

### Keep Operator Executions and let `ploeg-worker` claim their Run

* Good, because consumers keep their current protocol.
* Bad, because it keeps the placeholder rows, the scheduler exclusions and a second lifecycle for work that ordinary dispatch can run.

### Create a Tracker Item and ingest it

* Good, because the tracker would own the content, as it does for tracker work.
* Bad, because it needs a tracker for every consumer and a write path into it, and `design.md` §2 says manual Admission never silently creates a Tracker Item.

## Re-evaluation triggers

* A consumer needs work that does not fit a Team's plan, such as a single review Round or a plan override per Admission.
* Polling the operator event feed measurably loads ploegd, which would call for a push feed.
* ADR-0100 or ADR-0101 is rejected, so admitted work can no longer be interactive.
* Operator Executions are still admitted on the review date.

## More Information

* Technical story: OpenSpec change `submit-work-items`.
* 2026-10-13 — Proposed. On 2026-10-13 the owner chose that Ploeg executes every session, that the Harness is chosen per session, and that interactivity is kept (Unfold ADR-0023, accepted 2026-10-11, and Unfold ADR-0044, proposed 2026-10-13).
* 2026-10-14 — Accepted. The owner delegated the open questions of `submit-work-items` to their recommendations; merging the change ratifies this record and the supersession of ADR-0024. `POST /api/v1/operator/work-items` is implemented, and the `harness`, `model`, `attended` and `autoApprove` fields are added by the changes that define them.
* Supersedes [ADR-0024](0024-operator-work-uses-one-execution-authority.md) (proposed). Ratify both together; if this record is rejected, set `supersedes: none`.
* 2026-10-14 — Checked against [ADR-0095](0095-work-is-handed-to-ploeg-by-assigning-one-ploeg-user-and-a-team-label-picks-the-team.md), proposed the same day: it routes tracker-originated work by assigning the Ploeg user and reading a team label. An Admission names its Team explicitly and never reads tracker labels or assignees, so the two paths do not overlap. This record is the "start work from Unfold" path that 0095 names as a re-evaluation trigger.
* Related: [ADR-0006](0006-ahp-is-the-wrong-layer.md), [ADR-0012](0012-two-level-budgets-authorized-and-settled.md), [ADR-0069](0069-ploeg-names-none-of-its-consumers.md), [ADR-0098](0098-an-operator-admission-may-choose-each-runs-harness-from-its-teams-allowlist.md), [ADR-0100](0100-an-attended-run-relays-its-harnesss-requests-through-ploegd-and-falls-back-to-its-teams-policy.md); rules R1, R2, R3, R5, R13, R18.
