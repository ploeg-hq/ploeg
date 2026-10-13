## MODIFIED Requirements

### Requirement: A worker claims work for its Role only

`POST /api/v1/claim` SHALL accept an optional `role`. With a role, the claim
returns the oldest pending Run for that `(team, role)`; without one, behaviour
is unchanged. When the claim request names a `harness` (and `harnessProfile`),
the claim SHALL return only a pending Run whose stored Harness equals it or
that stores none, comparing the key derived from `harness` and
`harnessProfile` as the chart derives it (an empty `acp` profile is `opencode`). The response SHALL carry `shift`, `role`, `round`, `branch`,
`writes`, the authorized budget and, when the Run stores one, its resolved
`model`; the worker SHALL call that model instead of its configured one.

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

#### Scenario: A worker claims only its own Harness

- **GIVEN** pending builder Runs storing `acp/opencode` and `openhands`
- **WHEN** a builder worker running `openhands` claims
- **THEN** it receives the `openhands` Run and the other stays pending

#### Scenario: A claim carries the chosen model

- **GIVEN** a pending builder Run that stores the model `kimi-k3`
- **WHEN** a builder worker whose environment names `deepseek-v4` claims it
- **THEN** the claim response names `kimi-k3` and the Harness is started with `kimi-k3`

#### Scenario: Pod dies after claiming a selected Harness

- **WHEN** a worker claims a Run storing `acp/opencode` and its pod is killed
- **THEN** expiry recovers the Run by its lane's rules as today (R2, R5) and any retry stores the same Harness

### Requirement: The scale signal and the claim use one predicate

The KEDA scaler query for a Role and Harness and `Store.PendingRuns` SHALL
select over the identical predicate as `ClaimRole`, including the Harness
filter. Overshoot is acceptable; undershoot stalls Work Items silently and
forever, so the two SHALL be tested against each other. The HTTP route
`GET /api/v1/queue/depth` was removed on 2026-09-23 because nothing consumed
it.

#### Scenario: Depth agrees with what can actually be claimed

- **GIVEN** any set of pending Runs
- **WHEN** depth is read for a `(team, role, harness)` and then every claim is drained
- **THEN** the number claimed equals the depth reported

#### Scenario: Scaling to zero is normal

- **GIVEN** a Role with no pending Runs
- **THEN** depth is zero and no pod exists for it

#### Scenario: Every allowed Harness has a workload

- **GIVEN** a Role whose allowlist has two Harnesses
- **WHEN** the chart is rendered
- **THEN** there is one scaled workload per Harness, each with the scaler query for its Harness
