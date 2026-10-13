---
status: proposed
date: 2026-10-13
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# An operator admission may choose each Run's Harness from its Team's allowlist

## Context and Problem Statement

A Run's Harness is fixed by the worker deployment of its Team and Role. The owner wants a person to choose the Harness per session in an operator consumer, with Ploeg's worker executing it. Who may choose which Harness, when is the choice fixed, and how does it stay within a Team's governance and budget?

## Decision Drivers

* A Team, not the person in the dialog, decides which agents may run against its repositories and with its credentials.
* A choice that cannot be executed must be refused at admission, never queued.
* A retry and a later Round of the same session must run the same Harness.
* Budgets stay per Run from the Shift pool ([ADR-0012](0012-two-level-budgets-authorized-and-settled.md)). Which model a Run calls is decided next to the Harness in [ADR-0103](0103-an-operator-admission-may-choose-each-runs-model-from-the-models-its-roles-policy-lists.md), with the same per-Role resolution.

## Considered Options

* The admission names a Harness; ploegd checks it against the Team's allowlist and qualification and stores the resolved Harness on each Run when the Shift creates it
* The admission names a Harness and the worker that claims decides whether it can run it
* Keep the Harness per Role and let a person choose a Role instead

## Decision Outcome

Chosen option: "the admission names a Harness, ploegd checks and stores it per Run", because it refuses what cannot run before anything is created and keeps the claim a plain comparison.

* A Team lists allowed Harnesses (Harness and, for `acp`, profile) and a default per Role; a Role may narrow the list. A Team without a list allows only its configured Harness.
* A submission through the operator work-item route that `ploeg-worker` executes (`POST /api/v1/operator/work-items`, [ADR-0102](0102-an-operator-consumer-admits-work-items-that-ploeg-worker-executes.md)) may name one Harness; an Operator Execution admission, whose Run the consumer executes, may not. ploegd refuses a Harness outside the allowlist, or unqualified and not the Role's default when the Team has not set `allowUnqualifiedHarnesses`. The Role's default is always admissible. A Team without a plan may name only its configured Harness. The Harness is part of the submission's idempotency fingerprint (R13).
* When the Shift creates a pending Run, ploegd stores the Harness it must use: the requested one if its Role allows it, else the Role's default. Retries and later Rounds resolve the same way.
* Qualification is a list kept in the chart and changed only when a live conformance run passes. The Teams read shows the allowlist, the default and each entry's qualification.
* Tracker-originated work and Harnesses with their own payer (`claude-environment`, [ADR-0084](0084-a-claude-code-run-can-execute-in-a-teams-self-hosted-claude-environment.md)) are outside this choice.

### Consequences

* Good, because a person can pick an agent and a Team still bounds the choice.
* Good, because an impossible choice is a client error, not a stalled Work Item.
* Bad, because a Role whose allowlist lacks the chosen Harness runs its default, so one session may run two Harnesses; the admission response lists them.
* Bad, because qualification is a hand-kept list.

### Confirmation

* `go test ./pkg/httpapi/ ./pkg/store/` admits inside and outside the allowlist, with and without the unqualified opt-in, replays with another Harness (conflict), and checks the stored Harness on builder, reviewer, retry and re-opened Round Runs.
* `docs/contracts/operator-api.v1.schema.json` carries the admission field and the Teams read fields, checked by the operator contract tests.

## Pros and Cons of the Options

### The worker decides at claim

* Good, because ploegd needs no Harness configuration.
* Bad, because a Harness no worker can run is only discovered after admission, and the Run waits or fails against a retry budget.

### Choose a Role instead

* Good, because nothing new is stored.
* Bad, because Roles multiply per Harness and a plan's Rounds would have to change per session.

## Re-evaluation triggers

* A consumer needs a different Harness per Role in one session.
* Tracker work needs a per-Work-Item Harness, for example through a label.
* A payer-aware rule for `claude-environment` is wanted.

## More Information

* OpenSpec change `select-harness-per-run`. Depends on [ADR-0097](0097-a-run-records-its-harness-version-and-the-teams-read-shows-each-roles-harness-and-model.md) and on [ADR-0102](0102-an-operator-consumer-admits-work-items-that-ploeg-worker-executes.md) (proposed, OpenSpec change `submit-work-items`), which adds the submission route.
* 2026-10-13: the owner decided that the Harness is chosen freely per session (per Run), not fixed per Team or Role, and that Ploeg's worker executes every session (Unfold ADR-0002, Unfold ADR-0023). Proposed.
* 2026-10-13 — The owner decided that a session may also choose its model from the Team's list; [ADR-0103](0103-an-operator-admission-may-choose-each-runs-model-from-the-models-its-roles-policy-lists.md) records it and narrows ADR-0039. The Harness and the model are resolved per Role the same way.
