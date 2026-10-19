---
status: proposed
date: 2026-10-20
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A canary Team's stuck Run is a deliberate stop, not stuck work

## Context and Problem Statement

Team copper runs the `exec` harness as a free canary: for a Work Item titled `[canary] notes loop <time>` it writes one live note, waits and reports `stuck`, so the worker also writes its stop note, and the canary script requeues and finally withdraws the Work Item. On 2026-10-18 Work Items 229 to 235 did this by design. Every such Run counted in the operator summary's `stuck`: copper showed 8 stuck Runs and the totals 29 over 30 days, and each stop parked its Work Item with "shift stopped:  reported stuck in round 1 — ploeg-canary stopped on purpose", a reason with a double space where the Role of a Team without a plan is empty.

How does Ploeg tell a deliberate canary stop from stuck work without matching titles or summaries?

## Decision Drivers

* The stuck count is read as "Runs that need a person"; probes must not inflate it.
* The marker must come from configuration an operator owns, never from text an agent or a tracker writes, so no Run can hide a real stop by calling itself a canary.
* A canary that fails for real (`failed`, an infrastructure fault) is exactly what a canary exists to show and must still count.
* The canary script requeues its Work Item from `needs_human`, so the state it parks in stays as it is.

## Considered Options

* A Team-level `canary: true` in the roster, recorded on each Run when ploegd claims it
* A `canary` field in the outcome report
* Match the `[canary]` title, the `notes-canary-*` tracker id or the stuck reason

## Decision Outcome

Chosen option: "A Team-level `canary: true` in the roster, recorded on each Run when ploegd claims it", because the roster is operator configuration and the flag is written once per Run, like `shadow` ([ADR-0096](0096-a-shadow-reader-records-its-verdict-and-never-acts.md)), so a later configuration change does not relabel history.

* `teams.<team>.canary: true` marks a Team whose Runs are probes. ploegd records `agent_runs.canary` (migration `0069_run_canary.sql`) from it when it records the Run's configuration ([ADR-0091](0091-each-run-records-the-model-harness-plan-and-ploegd-version-it-was-configured-with.md)); once true it stays true.
* A canary Run that finished `stuck` is a canary stop. The operator summary leaves it out of `finished` and `stuck` and counts it in `canaryStops`. A canary Run with any other outcome counts as before.
* The shift engine still closes the Shift and parks the Work Item at `needs_human`, but the close reason is `canary stopped: <role> round <n>` and the needs_human reason says the stop was on purpose and nothing needs a person.
* Every Run shape carries `canary`.
* A stuck reason names a Run without a Role by what it does, `writer` or `reader`, so it never reads "shift stopped:  reported stuck".

### Consequences

* Good, because the stuck count and the needs_human reasons describe work again, and a broken canary still shows as failed.
* Good, because no title, tracker id or agent text decides anything.
* Bad, because the deployment must set `canary: true` on copper; until it does, its stops count as stuck as before.
* Bad, because `ploeg_runs_finished_total{outcome="stuck"}` still counts canary stops under the canary Team's label; no alert reads stuck outcomes today.
* Neutral, because a canary Work Item waits at `needs_human` for the seconds between its stop and the script's requeue, and stays there if the script dies; that is a canary a person should look at.

### Confirmation

* `go test ./pkg/config/` proves only a Team with `canary: true` is a canary Team.
* `go test ./pkg/httpapi/` proves a claimed Run of a canary Team is recorded as a canary on every operator read, and another Team's is not, within `operator-api.v1.schema.json`.
* `go test ./pkg/store/` proves the summary counts a canary stop in `canaryStops` and not in `finished` or `stuck`, still counts a failed canary Run as failed, and never clears a recorded flag.
* `go test ./pkg/shiftengine/` proves a canary stop closes as `canary stopped: builder round 1` with a needs_human reason that says nothing needs a person, and that a Run without a Role is named `writer` or `reader`.

## Pros and Cons of the Options

### A `canary` field in the outcome report

* Good, because the harness that stops on purpose says so itself.
* Bad, because the outcome report is written by the harness the agent runs in: any agent could mark a real stop as a canary and hide it from a person.

### Match the title, tracker id or stuck reason

* Good, because it needs no configuration change.
* Bad, because a tracker author or an agent writes those strings, and a rename silently breaks the count.

## Re-evaluation triggers

* A second canary needs to stop on purpose with another outcome than `stuck`.
* A Team mixes probe Runs with real work, so a Team-level flag is too coarse.
* An alert or dashboard starts reading `ploeg_runs_finished_total{outcome="stuck"}`.

## More Information

* 2026-10-20 — Proposed after the 2026-10-18 notes canary (Work Items 229 to 235) counted every deliberate stop as stuck.
* Contract: `docs/contracts/operator-api.v1.schema.json`, `summaryRuns.canaryStops` and the Run shapes' `canary`.
* Related: [ADR-0091](0091-each-run-records-the-model-harness-plan-and-ploegd-version-it-was-configured-with.md), [ADR-0096](0096-a-shadow-reader-records-its-verdict-and-never-acts.md).
