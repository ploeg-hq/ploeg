# Run reservation

## ADDED Requirements

### Requirement: ploegd serves the executor's demand

ploegd SHALL serve `GET /api/v1/executor/demand?team=&role=` to a bearer token scoped to demand reads. It SHALL answer the number of Runs an executor should start: the eligible pending Runs, bounded by the Team's `maxRunning` less its running and reserved Runs.

The pending count SHALL come from the same predicate as `ClaimRole`.

#### Scenario: Demand respects the cap

* **WHEN** a Team with `maxRunning: 2` has one running Run, one reserved Run and three pending Runs
* **THEN** demand for that Team is 0

#### Scenario: Demand agrees with the claim

* **WHEN** demand reports N for a Team and Role
* **THEN** N successive reservations for that Team and Role succeed, and the next one answers 204

### Requirement: A launcher reserves a specific Run before starting a sandbox

An executor SHALL call `POST /api/v1/runs/reserve {team, role, launchRef}` before it creates a sandbox. ploegd SHALL move the oldest eligible pending Run to `reserved` under the Team's capacity lock and return its reservation id, or answer 204 when no Run is eligible.

A reservation SHALL count against `maxRunning`. It SHALL NOT create a Lease, authorize spend or mint a credential.

`launchRef` SHALL be stored as an opaque string.

#### Scenario: Concurrent reservers respect the cap

* **WHEN** two launchers reserve concurrently for a Team with `maxRunning: 1` and two pending Runs
* **THEN** exactly one Run is reserved, and the other launcher receives 204

#### Scenario: A launcher that dies releases its Run

* **WHEN** a reservation is not renewed within its TTL
* **THEN** the sweep returns the Run to `pending`, records no failed Run and consumes no retry budget

### Requirement: Missing capacity is a wait on the reserved Work Item

While the reserved Run's sandbox cannot be scheduled, the launcher SHALL keep waiting and report `{reason, since}` to ploegd. ploegd SHALL expose the wait on the reserved Work Item through the operator API and `/metrics`.

No Run SHALL fail because the cluster has no capacity.

A template, image-pull or pod failure SHALL fail the reserved Run with failure reason `infra_node`.

#### Scenario: Full cluster

* **WHEN** a reserved Run's sandbox stays unschedulable for 30 minutes
* **THEN** the Work Item shows "waiting for capacity" with the scheduler's reason, no Run of any Work Item fails, and `ploeg_reservations_waiting_capacity` counts it

#### Scenario: The old failure does not recur

* **WHEN** the sandbox of Work Item A cannot start and Work Item B has a pending Run
* **THEN** Work Item B's Run stays pending and is not failed

### Requirement: The worker binds the Run it was launched for

A worker in a reserved sandbox SHALL read the reservation id from a downwardAPI volume and call `POST /api/v1/runs/bind`. ploegd SHALL mint the Lease and capabilities at bind.

A worker without an id SHALL wait up to the reservation TTL, and SHALL NOT claim any other Run.

#### Scenario: Warm pod adopted

* **WHEN** a claim adopts a warm pod and its reservation annotation appears
* **THEN** the worker binds that Run and starts its harness

#### Scenario: Stale reservation

* **WHEN** a worker binds a reservation that has expired or was released
* **THEN** the bind is refused, and no Lease or credential is minted

### Requirement: KEDA reads no database

The chart SHALL render the sandbox executor's ScaledJob trigger as KEDA's `metrics-api` scaler with bearer authentication against the demand endpoint. It SHALL render no SQL query and no database credential for KEDA.

#### Scenario: Golden render

* **WHEN** the chart is rendered with `executor.type: sandbox`
* **THEN** the ScaledJob trigger is `metrics-api`, and the TriggerAuthentication references only the demand token Secret
