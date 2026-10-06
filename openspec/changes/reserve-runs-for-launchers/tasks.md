# Tasks — launch-sandboxes-from-ploegd

Planning-only change: nothing below is implemented yet. Each step is at most a day and names the test that checks it.

## 1. Store

- [ ] 1.1 Next migration: `run_launches` table and the `launching` Run state, with a CHECK constraint update
  Test: `pkg/store` migration test applies the migration on embedded Postgres; `TestRunStates_MatchCheckConstraint` lists every state.
- [ ] 1.2 `Dispatch(team)`: capacity lock, oldest eligible pending Run across Roles by Work Item age, move to `launching`, insert launch row
  Test: `TestDispatch_ConcurrentRespectsCap` (two goroutines, `maxRunning: 1`, two pending Runs; one launches) and `TestDispatch_OrdersByWorkItemAge`.
- [ ] 1.3 `launching` counts against `maxRunning` in the existing claim path
  Test: `TestClaim_CountsLaunchingAgainstCap`.
- [ ] 1.4 `Bind(launchID, attempt)`: validate the launch, move the Run to `running`, create the Lease, and hand off to existing capability minting
  Test: `TestBind_MintsOnceForCreatedLaunch`, `TestBind_RefusesReleasedOrWrongAttempt`.
- [ ] 1.5 `MarkWaiting`, `Requeue` with backoff, `Release` and the launch deadline
  Test: `TestRequeue_ReturnsRunToPendingWithoutFailedRun`.

## 2. agent-sandbox client

- [ ] 2.1 `pkg/sandboxlaunch`: in-cluster auth from the mounted token and CA; create with deterministic name and labels; `AlreadyExists` counts as success
  Test: `httptest` fake; `TestCreate_AlreadyExistsIsSuccess`.
- [ ] 2.2 List claims by label selector, and read a Sandbox's `PodScheduled` condition
  Test: `TestSandboxScheduling_UnschedulableReason` against a fixture captured from v1.0.5.
- [ ] 2.3 Patch SandboxWarmPool `/scale`
  Test: `TestScaleWarmPool_PatchesReplicas`.
- [ ] 2.4 CRD contract fixtures for v1.0.5 claims, sandboxes and warm pools
  Test: `TestCRDFixtures_Decode` fails on a renamed field.

## 3. ploegd loops

- [ ] 3.1 Dispatcher tick per Team on the sandbox Executor
  Test: `cmd/ploegd` wiring test with a fake store.
- [ ] 3.2 Launch outbox worker
  Test: `TestOutbox_ReplaysAfterCrash` (commit, stop before create, restart; exactly one create).
- [ ] 3.3 Reconciler: orphan deletion, missing-claim requeue on two consecutive lists, launch deadline, capacity waits, warm pool targets
  Test: `TestReconcile_DeletesOrphan`, `TestReconcile_RequeuesVanishedClaim`, `TestReconcile_UnschedulableIsWaitNotFailure`.
- [ ] 3.4 Metrics `ploeg_launches_waiting{team}` and `ploeg_launch_wait_seconds`, and a PrometheusRule alert
  Test: `pkg/store/metrics` exposition test; chart golden render for the rule.

## 4. Run API and worker

- [ ] 4.1 `POST /api/v1/runs/bind` and its schema in `docs/contracts/run-api.v1.schema.json` (additive)
  Test: `pkg/harness/contract_test.go` validates the bind request and response; an `httpapi` handler test.
- [ ] 4.2 Worker reads the launch identifier from the downwardAPI volume, waits for it up to the deadline, then binds
  Test: `pkg/worker` test with a temporary file that appears after a delay.
- [ ] 4.3 Remove `FailUnstartedRun` from the sandbox path
  Test: `TestSandboxWorker_NeverClaimsOtherRun`.
- [ ] 4.4 Operator API exposes the waiting reason (additive optional field in `operator-api.v1.schema.json`)
  Test: `pkg/httpapi/operator_test.go` schema validation of a waiting Work Item.

## 5. Chart and docs

- [ ] 5.1 `executor.type: sandbox` drops the ScaledJob, TriggerAuthentication and launcher Job; adds the Role, RoleBinding, ResourceQuota, admission policy and downwardAPI volume
  Test: golden renders updated; `mise run verify` Helm checks pass.
- [ ] 5.2 `docs/contracts/executor.md`, `docs/architecture.md` §3–4, `docs/ops/managed-workers.md` and `docs/ops/alerts.md` describe push launch
  Test: `mise run docs-check` (if present), with links resolving.
- [ ] 5.3 Homelab follow-up handed over as an ordered command set: remove the KEDA-to-database NetworkPolicy opening, and narrow or retire `ploeg_scaler`
  Test: not in this repository; verified in homelab-cluster with flux-local.
