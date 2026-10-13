## Why

The owner decided on 2026-10-13 that `ploeg-worker` executes every session an Operator Consumer starts (Unfold ADR-0023, accepted 2026-10-11; Unfold ADR-0044, proposed 2026-10-13). Ploeg cannot do that today. Its only operator Admission, `POST /api/v1/operator/executions` (`pkg/httpapi/operator_execution.go:39,86-141`), makes the consumer the Executor: `AdmitOperatorExecution` writes an `operator_owned` Work Item, an `operator/<session>` Shift, a `running` Run with Role `operator` and its Lease in one transaction (`pkg/store/operator_execution.go:92-163`), and no worker ever claims that Run. The scheduler skips these placeholder rows in ten predicates (`pkg/store/shift.go:56,115,259,436,560,624,650,688,775,917`). There is no route through which a consumer admits a manual-origin Work Item into the Team Queue, so `select-harness-per-run` (design Open Question 1) and `interactive-runs` have no admission to attach to.

## What Changes

- **New operator route (proposed):** `POST /api/v1/operator/work-items` admits a manual-origin Work Item for an authenticated Operator Consumer with `execute` permission and the Team in its scope, reusing the consumer registry from `PLOEG_OPERATOR_CONSUMERS` (`pkg/httpapi/operator.go:24-125`). The Work Item is not `operator_owned`: it enters its Team Queue, its Shift opens from the Team's plan, and `ploeg-worker` claims its Runs exactly as it does for tracker-originated work (R1, R2).
- **Idempotency:** a consumer-chosen `requestId` identifies the Admission. A replay with the same payload returns the existing Work Item and never re-queues it; a replay with another payload is a conflict. The route MUST NOT reuse `IngestAssigned`, whose conflict path re-queues settled work (`pkg/store/store.go:182-230` through `queueAssigned`, `pkg/store/requeue.go:57-70`).
- **Budget ceiling:** an optional `budgetUsd` is capped by the consumer's `maxBudgetUsd` (`pkg/httpapi/operator.go:96-102`) and stored as the Work Item's allotment, which already caps the Shift pool (`pkg/store/shift.go:62-64`).
- **Work Target:** the Admission names a registered target key (`pkg/target/resolver.go:397`), never a repository URL, and is refused when the target is not ready (`pkg/target/readiness.go:55`).
- **Follow and steer with existing routes:** `GET /api/v1/operator/events?workItemId&after` (`pkg/httpapi/operator.go:306-335`) already serves the Admission's lifecycle; cancel is the existing withdraw (`pkg/httpapi/withdraw.go:123-187`), try again is the existing requeue (`pkg/httpapi/requeue.go:36-87`, ADR-0044) and a message is the existing note (`pkg/httpapi/operator_notes.go:47-112`, ADR-0068). This change adds one audit action for the Admission and states how each existing route treats a manual-origin Work Item.
- **Extension points:** the request reserves the optional `harness` and `model` fields that `select-harness-per-run` defines (ADR-0098, ADR-0103) and the `attended` and `autoApprove` fields that `interactive-runs` defines (ADR-0100). All four enter the idempotency fingerprint; none is specified here.
- **Retirement path:** Operator Executions (`/operator/executions/*`, their placeholder rows and the scheduler exclusions) retire once no consumer admits them and no open row remains. This change states the order and the precondition; the deletion itself is a later change (design Open Question 1).
- **ADR:** a proposed record supersedes ADR-0024 (proposed) where it makes the consumer the delegated Executor.

## Capabilities

### New Capabilities

- `operator-work-admission`: how an Operator Consumer admits a manual-origin Work Item into its Team Queue: authorization, idempotency, budget ceiling, Work Target, the hand-off to Shift dispatch, the Admission's audit event and its crash behaviour.

### Modified Capabilities

- `operator-api`: "Common execution admission" covers Work Item Admission alongside Operator Executions, and a new requirement fixes how withdraw, requeue and notes apply to an admitted manual-origin Work Item and when Operator Executions stop being admitted.

## Seams

Run API (the operator routes in `pkg/httpapi`) and Store (one new function and one append-only migration in `pkg/store`). They stay one change because an Admission is one transaction; the specs keep the route contract (`operator-api`) apart from the Admission's store semantics (`operator-work-admission`). The Executor, the worker, the Harness and LLM control are not touched: admitted work reaches them through the existing claim.

## Dependencies

- None to land. `select-harness-per-run` and `interactive-runs` depend on this route for operator sessions; `record-harness-per-run` is independent of it.
- No backlog item covers it. Backlog #101 (live run surface) stays blocked and outside Ploeg (ADR-0006).

## Non-goals

- Executing anything in the consumer. The consumer submits, reads and commands; it never holds a Lease or a model key for admitted work.
- Choosing a Harness or a model, or marking work attended or auto-approved. Those fields belong to `select-harness-per-run` and `interactive-runs`.
- Pause and resume, and messages that reach a running Run. `interactive-runs` (ADR-0100, ADR-0101, proposed) owns them per attended Run.
- Push delivery (SSE or webhooks) of events. Polling the existing snapshot feed is the contract until it is measured to be insufficient.
- Binding a Tracker Item. Tracker-originated work keeps arriving through its tracker (R16, `docs/contracts/tracker-execution.md`); the Admission creates no Tracker Item (`design.md` §2).
- Priority. A manual-origin Work Item carries no tracker priority; the Team Queue orders it oldest-first (R10).
- Deleting Operator Executions, their tables or the scheduler exclusions.
- Naming any consumer in code or contracts (ADR-0069).

## Impact

- `pkg/httpapi`: a new handler and route next to `operator.go:181-201`; error mapping in the style of `operator_execution.go:86-141`.
- `pkg/store`: a new Admission function, and a new migration after `0052_work_item_state_active.sql` for the Admission record (consumer, `requestId`, actor, fingerprint, Work Item).
- `pkg/shiftengine`: called, not changed (`Engine.EnsureShift`, `engine.go:83-109`).
- `docs/contracts/operator-api.v1.schema.json` and its Go types, `docs/contracts/README.md` (operator section), `docs/architecture.md` §1-§3.
- Unfold: moves from `/operator/executions` to the new route under its own ADR-0023 increments; this repository changes nothing there.
