## MODIFIED Requirements

### Requirement: Common execution admission

Governed operator work SHALL be admitted under Ploeg authority with stable Work Item and execution identity. Commands MUST be idempotent for the authenticated actor and MUST reject incompatible payload reuse or stale revisions. A disconnected human MUST NOT cause a second execution. Operator work SHALL be admitted either as a manual-origin Work Item that `ploeg-worker` executes, through `POST /api/v1/operator/work-items`, or, until it is retired, as an Operator Execution that the consumer executes. The Work Item Admission request and response SHALL be published in `docs/contracts/operator-api.v1.schema.json` together with their Go types, as an additive v1 change.

#### Scenario: Lost start response
- **WHEN** an identical start command is repeated after a lost response
- **THEN** it identifies the existing execution without submitting a second paid turn

#### Scenario: Lost Work Item Admission response
- **WHEN** an identical Work Item Admission is repeated after a lost response
- **THEN** it identifies the existing Work Item without queuing it again or creating a Run

## ADDED Requirements

### Requirement: Existing commands steer an admitted Work Item

An admitted manual-origin Work Item SHALL be withdrawn by `POST /api/v1/operator/work-items/{id}/cancel`, restarted by `POST /api/v1/operator/work-items/{id}/requeue` (ADR-0044) and given a message for its next Run by `POST /api/v1/operator/work-items/{id}/notes` (ADR-0068), with the same authorization, idempotency and preconditions those routes apply to tracker-originated work. Withdrawing it MUST NOT write to any tracker. A withdrawn admitted Work Item MUST NOT be queued again by a replay of its Admission. A pending cancellation or an operator stop MUST NOT become an automatic retry (R3, R14).

#### Scenario: Cancel a running admitted Work Item
- **WHEN** a consumer cancels an admitted Work Item whose builder Run is running
- **THEN** the Shift closes with reason `withdrawn_by_operator`, pending Runs are cancelled, the running Run's model key is blocked, its next Lease renewal is refused, the Work Item is `withdrawn`, and no tracker is called

#### Scenario: Cancel races the failed-writer rule
- **WHEN** a cancelled writing Run reports a failed Outcome after its Shift was closed by the cancel
- **THEN** no Round is re-opened and the Work Item stays `withdrawn`

#### Scenario: Message for the next Run
- **WHEN** a consumer adds a note to an admitted Work Item while its first Run is running
- **THEN** the next Run of that Work Item is briefed with the note and the running Run is not

#### Scenario: Try again after needs_human
- **WHEN** a consumer requeues an admitted Work Item that is `needs_human`
- **THEN** a new Shift opens from the chosen Round within the consumer's `maxBudgetUsd`

### Requirement: Operator Executions retire after their last consumer moves

While Operator Executions remain, their routes, placeholder rows and scheduler exclusions SHALL keep their current behaviour, and an admitted manual-origin Work Item MUST NOT depend on any of them. ploegd SHALL stop admitting new Operator Executions only through a later change, and SHALL remove their tables and the scheduler exclusions only after no Operator Execution remains open.

#### Scenario: Both admissions coexist
- **WHEN** one consumer admits an Operator Execution and another admits a manual-origin Work Item for the same Team
- **THEN** a worker claims only the admitted Work Item's Runs, and the Operator Execution's Run remains the consumer's

#### Scenario: Admitted work ignores the exclusions
- **WHEN** the scheduler predicates that skip Operator Executions are evaluated for an admitted Work Item
- **THEN** none of them excludes it, because it is neither operator-owned nor linked to an Operator Execution
