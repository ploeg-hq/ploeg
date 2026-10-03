# role-claim Specification

## Purpose
How a worker claims work for its Role: the claim and the KEDA scale signal
select with one predicate, a Team's `maxRunning` cap bounds its claims, and only
writing Runs take the Shift's Lease. Archived from the change
`2026-07-29-run-multi-agent-shifts`.
## Requirements
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

The KEDA scaler query for a Role and `Store.PendingRuns` SHALL select over
the identical predicate as `ClaimRole`. Overshoot is acceptable; undershoot
stalls Work Items silently and forever, so the two SHALL be tested against
each other. The HTTP route `GET /api/v1/queue/depth` was removed on
2026-09-23 because nothing consumed it.

#### Scenario: Depth agrees with what can actually be claimed

- **GIVEN** any set of pending Runs
- **WHEN** depth is read for a `(team, role)` and then every claim is drained
- **THEN** the number claimed equals the depth reported

#### Scenario: Scaling to zero is normal

- **GIVEN** a Role with no pending Runs
- **THEN** depth is zero and no pod exists for it

### Requirement: A team's concurrency cap bounds its claims

A team MAY carry a concurrency cap, `maxRunning`. While the team has that many
running Runs, excluding operator executions, a claim for any of its Roles
SHALL return no work exactly like an empty queue. Unset or `0` SHALL mean
unlimited. Concurrent claims SHALL NOT exceed the cap together.

#### Scenario: Concurrent claims stop at the cap

- **GIVEN** a team with `maxRunning: 3` and eight pending Runs
- **WHEN** eight workers claim at the same moment
- **THEN** three receive a Run, five exit 0 empty-handed
- **AND** five Runs remain pending

#### Scenario: A finished Run releases its slot

- **GIVEN** a team at its cap
- **WHEN** one of its Runs finishes or expires
- **THEN** the next claim succeeds

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

