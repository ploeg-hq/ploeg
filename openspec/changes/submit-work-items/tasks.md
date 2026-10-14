## 1. Before implementation

- [x] 1.1 Get the owner's answers to design Open Questions 1–7 and record them in `design.md`; ratify or reject ADR-0102 (and with it ADR-0024's supersession)
- [x] 1.2 Update `docs/domain/model.yaml` per Open Question 6 (Admission of a manual-origin Work Item, Operator Consumer, Work Item, Operator Execution marked retiring, the replay rule), regenerate the domain docs, and run `mise run docs-check` if available

## 2. Store

- [x] 2.1 Add the next numbered migration in `pkg/store/migrations/` (after `0052`, renumbered against whatever `record-harness-per-run`, `select-harness-per-run` and `interactive-runs` landed first) creating `operator_work_admissions(consumer, request_id, actor, fingerprint, work_item_id, created_at, PRIMARY KEY (consumer, request_id))`; never edit an applied migration
- [x] 2.2 Add `Store.AdmitOperatorWorkItem`: advisory lock on `consumer:requestId`, replay or `ErrRequestConflict` on a hit, otherwise insert a queued, not operator-owned `manual` Work Item with the resolved target and `budget_usd`, the admission row and the `operator.work_item.admitted` audit row in one transaction
- [x] 2.3 Store tests on a real PostgreSQL: replay returns the Work Item unchanged in `queued`, `done`, `needs_human` and `withdrawn`; a changed payload or actor conflicts; two concurrent first requests create one Work Item; a `requestId` equal to an existing Operator Execution's session id conflicts instead of adopting the operator-owned row
- [x] 2.4 Store test: `OpenShift` on an admitted item caps the pool at its allotment for a plan pool above it and for a synthesized pool of 0; every predicate in `shift.go` that excludes Operator Executions still returns the admitted item

## 3. Run API (operator routes)

- [x] 3.1 Add `Dispatches(team string) bool` (or equivalent) to `httpapi.ShiftEngine` and implement it in `pkg/shiftengine` over `planFor`
- [x] 3.2 Register `POST /api/v1/operator/work-items`: `executionActor`, `decodeExecution`, field validation, team scope, `budgetUsd` against `maxBudgetUsd`, registered target and `Gate.Admit` readiness, dispatchability; call the store, then `Engine.EnsureShift` with errors logged for the sweeper; map errors to 400, 403, 409 `request_conflict`, 422 and 503
- [x] 3.3 Allowlist `requestId` in `operatorEventJSON` (`pkg/store/operator.go`) so the Admission event reads through `/operator/events`
- [x] 3.4 Handler tests with `httptest`: read-only consumer, team outside scope, missing actor, unknown and unready target, budget above ceiling, unplanned team, lost-response replay (200, `replayed: true`, no second Shift), conflict
- [x] 3.5 Handler tests on admitted work: cancel withdraws without a tracker call and a later failed writer Outcome re-opens no Round; a note reaches the next Run only; requeue from `needs_human` stays within `maxBudgetUsd`; a replay after cancel leaves it `withdrawn`

## 4. Contract and end-to-end

- [x] 4.1 Add the Admission request and response to `docs/contracts/operator-api.v1.schema.json` with their Go types, additively, plus the operator contract test assertion; leave the `harness`, `model`, `attended` and `autoApprove` fields to their changes
- [x] 4.2 An end-to-end test with real PostgreSQL, the ploegd handlers and an in-process `worker.Worker` on the `exec` adapter: admit, the worker claims and finishes, the event feed shows the Admission through `shift.closed`, and exactly one key is minted per Run
- [x] 4.3 Crash test: stop after the Admission commit and before `EnsureShift`; the sweeper opens the Shift and a replay opens no second one

## 5. Docs

- [x] 5.1 Update `docs/contracts/README.md` (operator section: the route, idempotency, budget, target, which commands apply) and `docs/architecture.md` §1–§3 (manual-origin Admission executed by the worker; admitted Runs count against `maxRunning`)
- [x] 5.2 Note in `docs/architecture.md` §9 that Operator Executions remain until the retirement change

## 6. Gates

- [x] 6.1 Run the gate set from `.forgejo/workflows/on_pull_request.yml`: `gofmt -l .`, `go vet ./...`, `go build ./...`, `go test ./...`, `helm lint ops/helm/ploeg` and the three `helm template` renderings, and paste the output in the PR body
- [x] 6.2 Run `go test ./internal/ledger/` for ADR-0102 and its Records and Review calendar rows
- [x] 6.3 Confirm no claim or queue predicate changed, so `ops/helm/ploeg/templates/scaledjob.yaml` needs no edit; `TestClaimRoleAgreesWithPendingRuns` passes unchanged
