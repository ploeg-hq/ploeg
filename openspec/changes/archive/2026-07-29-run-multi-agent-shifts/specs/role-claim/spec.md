# Role-scoped claim

## ADDED Requirements

### Requirement: A worker claims work for its Role only

`POST /api/v1/claim` SHALL accept an optional `role`. With a role, the claim
returns the oldest pending Run for that `(team, role)`; without one, behaviour
is unchanged. The response SHALL carry `shift`, `role`, `round`, `branch`,
`writes` and the authorized budget.

#### Scenario: A reviewer pod claims only reviewer work

- **GIVEN** a Round with pending Runs for `builder` and `reviewer`
- **WHEN** a worker with `PLOEG_ROLE=reviewer` claims
- **THEN** it receives the reviewer Run
- **AND** the builder Run remains pending

#### Scenario: An empty queue is not an error

- **WHEN** a worker claims for a Role with no pending Run
- **THEN** it exits 0 having done nothing (backlog #49)

#### Scenario: A role-less claim keeps working

- **GIVEN** a Team with no plan and a worker with no `PLOEG_ROLE`
- **WHEN** it claims
- **THEN** it gets work exactly as before this change

### Requirement: The scale signal and the claim use one predicate

`GET /api/v1/queue/depth` SHALL accept a role and answer from
`Store.PendingRuns`, which SHALL select over the identical predicate as
`ClaimRole`. Overshoot is acceptable; undershoot stalls Work Items silently and
forever, so the two SHALL be tested against each other.

#### Scenario: Depth agrees with what can actually be claimed

- **GIVEN** any set of pending Runs
- **WHEN** depth is read for a `(team, role)` and then every claim is drained
- **THEN** the number claimed equals the depth reported

#### Scenario: Scaling to zero is normal

- **GIVEN** a Role with no pending Runs
- **THEN** depth is zero and no pod exists for it

### Requirement: Only writing Runs take a Lease

A claim for a writing Role SHALL take the Shift's Lease; a claim for a reading
Role SHALL take none, which is what allows a fan-out to run at once.

#### Scenario: Three readers claim concurrently

- **GIVEN** a Round of three reading Roles
- **WHEN** all three claim at the same moment
- **THEN** all three succeed and no Lease row exists

#### Scenario: A writer's Lease is exclusive

- **GIVEN** a writing Run holding the Lease
- **THEN** no other Run in that Shift can acquire one
