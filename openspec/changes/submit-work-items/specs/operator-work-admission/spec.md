## ADDED Requirements

### Requirement: Only an authorized consumer admits manual-origin work

ploegd SHALL admit a manual-origin Work Item only for an Operator Consumer that authenticated with its bearer credential, has `execute` permission, names the Team inside its Team scope, and sends exactly one valid `X-Ploeg-Actor`. The consumer registry SHALL be the one configured through `PLOEG_OPERATOR_CONSUMERS`; this change MUST NOT add a second registry or permission. A refused Admission MUST create no Work Item, Shift, Run, Lease or audit row for the Work Item.

#### Scenario: Read-only consumer
- **WHEN** a consumer without `execute` permission requests an Admission
- **THEN** ploegd refuses it as forbidden and creates nothing

#### Scenario: Team outside scope
- **WHEN** a consumer with `execute` permission requests an Admission for a Team outside its scope
- **THEN** ploegd refuses it as forbidden and creates nothing

#### Scenario: Missing actor
- **WHEN** an Admission request carries no `X-Ploeg-Actor` or more than one
- **THEN** ploegd refuses it as a client error and creates nothing

### Requirement: The Work Target comes from the registry

An Admission SHALL name its Work Target by the key of a registered target. ploegd MUST refuse an unknown key and a key whose target is not ready, and MUST NOT accept a repository URL, a forge credential or any other Work Target coordinate from the consumer (R8, R11). The resolved Work Target SHALL be pinned on the Work Item at Admission (R12).

#### Scenario: Unknown target
- **WHEN** an Admission names a target key that is not registered
- **THEN** ploegd refuses it as a client error and creates nothing

#### Scenario: Target not ready
- **WHEN** an Admission names a registered target whose last readiness check failed and a fresh check fails
- **THEN** ploegd refuses it and the refusal names the target as not ready

### Requirement: The budget never exceeds the consumer's ceiling

An Admission MAY carry `budgetUsd`. ploegd MUST refuse a `budgetUsd` that is not a positive finite amount or that exceeds the consumer's `maxBudgetUsd`. ploegd SHALL store the Work Item's allotment as `budgetUsd` when given, otherwise as the consumer's `maxBudgetUsd`, so that the Shift pool is the lower of the Team plan's pool and that allotment, and an unmetered plan pool is never unmetered for admitted work. Each Run SHALL still be authorized from the Shift pool by its Role's cap (ADR-0012).

#### Scenario: Budget above the ceiling
- **WHEN** a consumer whose `maxBudgetUsd` is 25 requests an Admission with `budgetUsd` 40
- **THEN** ploegd refuses it as a client error and creates nothing

#### Scenario: Plan pool above the ceiling
- **WHEN** a consumer whose `maxBudgetUsd` is 25 admits work without `budgetUsd` for a Team whose plan pool is 50
- **THEN** the Shift opens with a pool of 25

#### Scenario: Budget below the plan pool
- **WHEN** a consumer admits work with `budgetUsd` 10 for a Team whose plan pool is 50
- **THEN** the Shift opens with a pool of 10

### Requirement: An Admission is idempotent by its request identity

An Admission SHALL carry a `requestId` that is unique per consumer. ploegd SHALL record the consumer, `requestId`, actor and a fingerprint of the request in the same transaction that creates the Work Item. The fingerprint MUST cover every field that changes the work, including the optional fields other changes add to the request. A replay with the same `requestId`, actor and fingerprint SHALL return the existing Work Item and MUST NOT change its state, queue it again, open a Shift or create a Run, whatever state the Work Item is in. A replay with the same `requestId` and a different fingerprint or actor MUST be refused as a conflict and MUST leave the first Admission unchanged.

#### Scenario: Response lost, request repeated
- **WHEN** a consumer repeats an Admission after its first response was lost
- **THEN** ploegd returns the same Work Item, marks the response as a replay, and no second Work Item, Shift or Run exists

#### Scenario: Replay after the work settled
- **WHEN** a consumer repeats an accepted Admission after its Work Item became `done`, `needs_human` or `withdrawn`
- **THEN** ploegd returns the Work Item in its current state and does not queue it again

#### Scenario: Same request identity, different payload
- **WHEN** a consumer repeats a `requestId` with another title, target, budget, Harness, model, `attended` or `autoApprove`
- **THEN** ploegd refuses it as a conflict and the first Work Item is unchanged

#### Scenario: Two concurrent first requests
- **WHEN** two requests with the same consumer and `requestId` arrive at the same time
- **THEN** exactly one Work Item is created and the other request receives it as a replay or a conflict

### Requirement: Admitted work enters the Team Queue and is executed by a worker

An admitted Work Item SHALL have manual origin, SHALL NOT be operator-owned, and SHALL be `queued` for its Team when the Admission commits (R1). ploegd SHALL then open its Shift from the Team's plan through the same path tracker-originated work takes, and the Shift's Runs SHALL be claimed only by `ploeg-worker` through the Run API. ploegd MUST NOT create a Run, Lease or model key for the consumer, and the consumer MUST NOT receive a Run capability. ploegd SHALL refuse an Admission for a Team that no Shift can dispatch.

#### Scenario: Planned Team
- **WHEN** a consumer admits work for a Team with a plan
- **THEN** a Shift is open with the plan's first Round, its first Run is `pending`, and a worker of that Team and Role can claim it

#### Scenario: Team without a plan or uniform dispatch
- **WHEN** a consumer admits work for a Team that has no plan while uniform dispatch is off
- **THEN** ploegd refuses it and creates nothing

#### Scenario: Admitted Run counts against the Team's cap
- **WHEN** a Team at its `maxRunning` cap has an admitted Work Item's Run pending
- **THEN** the Run waits like any pending Run of that Team

### Requirement: A crash never loses or duplicates an Admission

The Work Item and its Admission record SHALL be committed in one transaction, and the response SHALL be sent only after that commit. If ploegd stops after the commit and before the Shift opens, the sweeper SHALL open the Shift, as it does for any queued Work Item without a live Shift. If a worker pod dies while it holds a Run of admitted work, the Lease SHALL expire and recovery SHALL follow R2 and R5 exactly as for tracker-originated work; the consumer's connection plays no part in it.

#### Scenario: ploegd stops between commit and Shift
- **WHEN** ploegd stops after committing an Admission and before opening its Shift
- **THEN** after restart the sweeper opens the Shift, and a replay of the Admission returns the same Work Item without opening a second Shift

#### Scenario: ploegd stops before commit
- **WHEN** ploegd stops before the Admission transaction commits
- **THEN** no Work Item exists, and the consumer's replay creates it once

#### Scenario: Worker pod dies mid-Run
- **WHEN** the pod running an admitted Work Item's Run is killed
- **THEN** its Lease expires, the Run is recorded as expired, and the Work Item follows the Team's recovery policy without any consumer action

#### Scenario: Consumer disappears
- **WHEN** the consumer stops polling after an Admission
- **THEN** the Work Item's Runs continue, finish and record their Outcomes

### Requirement: The Admission is visible in the consumer's event feed

ploegd SHALL write one audit event for each new Admission, attributed to the consumer and actor, carrying the Work Item, Team and `requestId` and no request content. A replay SHALL write no event. The event and the Work Item's subsequent lifecycle events SHALL be readable through the existing team-scoped operator event feed with `workItemId` and `after`, under its snapshot consistency.

#### Scenario: Follow an admitted Work Item
- **WHEN** a consumer polls the event feed with the admitted Work Item's id and the last cursor it saw
- **THEN** it reads the Admission event first and later the Shift, Round, Run and Outcome events of that Work Item, in id order

#### Scenario: Replay is silent
- **WHEN** a consumer replays an accepted Admission
- **THEN** the event feed gains no new event for it
