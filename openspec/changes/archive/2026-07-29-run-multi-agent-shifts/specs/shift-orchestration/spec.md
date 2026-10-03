# Shift orchestration

## ADDED Requirements

### Requirement: A queued Work Item gets exactly one live Shift

When a Work Item enters `queued`, ploegd SHALL open a Shift for the Team that
owns it, carrying the branch, the budget pool and round zero. Two Teams SHALL
never hold a live Shift on the same Work Item (R1 in its Shift form).

#### Scenario: First assignment opens a Shift

- **WHEN** an assignment webhook queues a Work Item for team `bronze`
- **THEN** a Shift exists for that item with `round = 0` and `closed_at` null
- **AND** its branch is derived from the item, not from Team config

#### Scenario: A second Team cannot open a competing Shift

- **GIVEN** a live Shift on Work Item 42
- **WHEN** another Team attempts to open one on the same item
- **THEN** the attempt fails at the database, not in application code

#### Scenario: A plan-less Team behaves exactly as today

- **GIVEN** a Team with no configured plan
- **WHEN** its Work Item is queued
- **THEN** one Round with one writing Role is opened
- **AND** the observable behaviour is identical to the pre-Shift dispatch path

### Requirement: Rounds advance only when every Run in them has finished

ploegd SHALL open the next Round only when the current Round has no Run in
`pending` or `running`. Advancement SHALL be derived from Run state, never
reported by an agent (R2 — the pipeline must not depend on an agent behaving
well).

#### Scenario: A Round with one live reader does not advance

- **GIVEN** a Round of three readers, two finished and one still running
- **WHEN** the orchestrator evaluates the Shift
- **THEN** no new Round is opened

#### Scenario: A completed reader Round advances to the writer Round

- **GIVEN** a Round of three readers that have all reported
- **WHEN** the orchestrator evaluates the Shift
- **THEN** the next Round in the plan opens with one writing Role
- **AND** the writer's Run receives the readers' findings

#### Scenario: A swept Run does not block its Round forever

- **GIVEN** a Run whose pod died without reporting
- **WHEN** `ExpireRuns` reclaims it
- **THEN** the Round can complete and the Shift advances

### Requirement: A Shift closes on plan exhaustion or a terminal Outcome

A Shift SHALL close when its Team's plan has no further Round, or when any Run
reports an Outcome that ends the work. A closed Shift SHALL record why, so
"why did this item stop" is a query rather than a reconstruction, and SHALL
release the item's live-Shift slot so a later re-mandate can open a fresh one.

#### Scenario: The plan runs out

- **WHEN** the last Round of a Team's plan completes
- **THEN** the Shift closes with a recorded reason
- **AND** the Work Item reaches `needs_human` so a person is asked to merge

#### Scenario: A stuck Outcome freezes the plan

- **GIVEN** any Run reports `stuck` with a reason (R4)
- **THEN** the Shift closes, no further Round opens, and the item goes
  `needs_human` carrying that reason

### Requirement: An exhausted budget pool parks the item rather than failing it

When `ClaimRole` raises `ErrBudgetExhausted`, ploegd SHALL move the Work Item
to `needs_human` with an R4 reason naming the spend. Retrying cannot fix
running out of money.

#### Scenario: The pool empties mid-Shift

- **GIVEN** a Shift whose remaining budget is below the viable floor
- **WHEN** the next Round would open
- **THEN** no Run is spawned, no attempt is burned, no credential is minted
- **AND** the item is `needs_human` with a reason naming the pool and the spend
