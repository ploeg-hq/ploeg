# Verdict-driven advancement

## ADDED Requirements

### Requirement: A reading Run may return a verdict

A reading Run MAY return `verdict` in its `OutcomeReport`, one of `approve` or
`request_changes`. Ploeg SHALL persist it with the Run and SHALL ignore a
verdict returned by a writing Role (ADR-0017).

#### Scenario: A reviewer asks for changes

- **WHEN** a reading Run reports `request_changes` with findings
- **THEN** the verdict and the findings are both recorded against that Run

#### Scenario: A writer cannot grade its own work

- **WHEN** a writing Run reports `request_changes`
- **THEN** the verdict is ignored and the plan advances as if none was given

#### Scenario: No verdict is not a request for changes

- **WHEN** a reading Run reports findings without a verdict
- **THEN** the Shift closes at plan exhaustion as it does today

### Requirement: A request for changes re-opens the plan's writer, bounded

When a plan's final Round completes and any reading Run in it reported
`request_changes`, ploegd SHALL re-open the plan's last writing Round with the
findings injected, followed by the review Round. Each such pair is one fix
round. The verdict SHALL decide whether a fix round is wanted; a wanted fix
round SHALL then be bounded by the Shift pool first and `maxFixRounds` second.
A Shift whose last review did not request changes SHALL close as approved or
as an exhausted plan, never with a reason naming the pool or the cap.

#### Scenario: A fix round opens and the writer sees why

- **GIVEN** a plan whose final Round is a reviewer that reported `request_changes`
- **WHEN** the orchestrator evaluates the Shift
- **THEN** the plan's writing Round re-opens
- **AND** the writer's briefing carries that reviewer's findings

#### Scenario: An approval ends the Shift

- **GIVEN** a reviewer that reported `approve`
- **THEN** no further Round opens and the Shift closes as `review_approved`
- **AND** the item reaches awaiting_review when a writer opened or updated the
  pull request, so a person is asked to merge

#### Scenario: An approval on the last allowed fix round is an approval

- **GIVEN** a Shift that has run `maxFixRounds` fix rounds, or whose pool
  cannot fund another
- **WHEN** the reviewer of its last Round reports `approve`
- **THEN** the Shift closes as `review_approved`, not with a reason naming the
  cap or the pool

#### Scenario: The cap stops a reviewer that never approves

- **GIVEN** a reviewer that reports `request_changes` every time
- **WHEN** `maxFixRounds` fix rounds have run
- **THEN** the Shift closes with a reason naming the cap

#### Scenario: Money stops the loop before the cap does

- **GIVEN** a Shift whose remaining pool cannot fund another Round
- **WHEN** a fix round would otherwise open
- **THEN** no Run is spawned, no attempt is burned, no credential is minted
- **AND** the item is needs_human with a reason naming the spend

### Requirement: The fix-round count is derived, never counted

The number of fix rounds run SHALL be derived from the Shift's round counter
against its plan's length, not stored in a column. A counter can disagree with
what actually happened; a derivation cannot.

#### Scenario: The count survives a restart mid-loop

- **GIVEN** a Shift two fix rounds in
- **WHEN** ploegd restarts and the sweeper evaluates it
- **THEN** the same number of fix rounds is derived and the cap still applies

### Requirement: A plan that cannot fix anything is refused at boot

A Team plan configuring `maxFixRounds` greater than zero with no writing Round
SHALL be rejected when the configuration is parsed, not when a Shift reaches
its final Round.

#### Scenario: A reader-only plan with fix rounds fails fast

- **WHEN** ploegd starts with a plan of readers only and `maxFixRounds: 2`
- **THEN** it refuses to start, naming the team
