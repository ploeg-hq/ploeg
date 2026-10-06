# Tasks — reserve-runs-for-launchers

Planning-only change: nothing below is implemented yet. Each step is at most a day and names the test that checks it.

## 1. Store

- [ ] 1.1 Next migration: the `reserved` Run state and its reservation columns, with an updated CHECK constraint
  Test: the migration applies on embedded Postgres; `TestRunStates_MatchCheckConstraint`.
- [ ] 1.2 `Reserve(team, role, launchRef)` under the capacity lock; reservations count against `maxRunning`
  Test: `TestReserve_ConcurrentRespectsCap`, `TestClaim_CountsReservedAgainstCap`.
- [ ] 1.3 Renewal, sweep expiry back to `pending`, `RecordWait`, `Release`
  Test: `TestReservationExpiry_ReturnsRunToPendingWithoutFailure`.
- [ ] 1.4 `Bind(reservation)`: today's claim transition for that Run
  Test: `TestBind_MintsOnce`, `TestBind_RefusesExpired`.
- [ ] 1.5 Regression for WI-138: a launcher whose sandbox never starts fails no other Work Item's Run
  Test: `TestCapacityWait_NeverFailsAnotherWorkItem`, which fails on today's `FailUnstartedRun` path.

## 2. API and contracts

- [ ] 2.1 `GET /api/v1/executor/demand` with a demand-only bearer
  Test: `TestDemand_RespectsCapAndAgreesWithClaim`.
- [ ] 2.2 `POST /api/v1/runs/reserve`, renewal, wait report and bind; additive `run-api.v1.schema.json`
  Test: `pkg/harness/contract_test.go` schema validation, and handler tests.

## 3. Launcher and worker

- [ ] 3.1 `pkg/sandboxlaunch`: reserve first, pass the id in `additionalPodMetadata`, wait on `Unschedulable`, fail only the reserved Run on a fault
  Test: an `httptest` agent-sandbox fake, `TestLauncher_UnschedulableIsAWait`, `TestLauncher_TemplateFaultFailsTheReservedRun`.
- [ ] 3.2 The worker binds from the downwardAPI file, waiting up to the TTL; delete `FailUnstartedRun`
  Test: `TestWorker_BindsTheReservedRun`, `TestWorker_NeverClaimsAnotherRun`.

## 4. Chart and alerts

- [ ] 4.1 The ScaledJob uses `metrics-api` with bearer authentication; the SQL query and the database secret are removed
  Test: golden renders, `helm lint`.
- [ ] 4.2 `PloegCapacityWaitLong`, `PloegReservationsExpiring` and `PloegScaledJobErrors` in `prometheusrule.yaml`, documented in `docs/ops/alerts.md`
  Test: `promtool` unit tests fire on a rising series and not on a flat one, plus a threshold mutation that must fail.

## 5. Handover

- [ ] 5.1 homelab-cluster: replace the KEDA-to-database NetworkPolicy opening with KEDA-to-ploegd, add the demand token Secret, and narrow `ploeg_scaler` to the exporter's tables using `trace_alias`
  Test: in homelab-cluster, with flux-local.
