---
status: accepted
date: 2026-10-01
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# An operator restarts stopped work by requeueing it from a Round they choose

## Context and Problem Statement

When Ploeg stops working a Work Item, the owner has no way to start it again
from Vloer. The operator API can cancel a live item and approve or reject a
proposed one (`pkg/httpapi/operator.go`), and nothing else. Vloer says so
itself: "Starting again from Vloer is proposed Ploeg work. Today only the
tracker can start a new attempt." (`apps/vloer/public/core/reasons.js`).

The tracker route is blunt. Re-assigning the task is a fresh human mandate:
`IngestAssigned` requeues the item, resets `attempts` and `infra_failures`, and
a new Shift starts from Round 1 (`pkg/store/store.go`). That is right for a
stale premise and wrong for a failed review. On 2026-09-29 the reviewer of
Shift 118 was stopped by the ACP watchdog after the builder had opened PR #45.
The only restart re-ran the builder (US$ 0,58 and 35 minutes the first time)
to get a review worth at most US$ 0,40. It also needed the owner to leave
Vloer, unassign and reassign a Vikunja user, and know that this is how it
works.

Vloer's Cancel button does not fill the gap. It shows on `needs_human`,
`stale` and `awaiting_review` items, but `WithdrawWorkItem` changes nothing
when no Shift is live (`pkg/store/withdraw.go`).

VIK-606 already asks for `POST /api/v1/operator/work-items/{id}/requeue`
(restart from Round 1 for `needs_human` and `stale`).
[ADR-0036](0036-stuck-work-reaches-the-owner-as-a-cited-proposal-not-an-agent-decision.md)
says its `requeue_with_amendment` builds on that route.
[ADR-0043](0043-a-failed-reading-run-is-retried-and-a-missing-review-closes-review-failed.md)
makes a failed reviewer retry itself, but when that retry runs out a person
still needs a way back in.

The question this record answers: what may an operator do to restart stopped
work, and what bounds it.

## Decision Drivers

* Unfold ADR-0002, and Ploeg ADR-0024 and ADR-0025: Ploeg holds every state
  transition and authorizes every Run. Vloer asks, and it never executes.
* R2 and ADR-0017: no agent may restart work. A restart is a person's act,
  audited as theirs.
* ADR-0012: money is bounded before it is spent. The owner sees the maximum
  spend before confirming.
* Pay only for the Rounds that need re-running. A failed reviewer should not
  cost a builder Run.
* `shifts.round` is the index into the Team's plan (ADR-0019). Anything that
  changes where a Shift is in its plan must keep that index true.
* One route, so VIK-606, ADR-0036's brief acceptance and a future `ploegctl`
  share the same checks.

## Considered Options

* **Extend VIK-606's requeue route with a starting Round: a new Shift on the
  same branch begins at the Round the operator chooses**
* Reopen the closed Shift at the failed Round
* A separate endpoint per action (retry review, retry round, start over, add
  budget)
* A Follow-Up Work Item per restart, like the forge repair Follow-Ups
* Keep the tracker as the only restart

## Decision Outcome

Chosen option: "extend VIK-606's requeue route with a starting Round", because
it gives the owner a restart that pays only for the Rounds that need
re-running. It keeps the closed Shift as the record, and it adds no new
authority: it is the tracker's fresh human mandate, reached through the
operator API.

### The route

`POST /api/v1/operator/work-items/{id}/requeue`, with an optional JSON body:

| Field | Meaning | Default |
| --- | --- | --- |
| `fromRound` | The planned Round the new Shift starts at, counted from 1 | 1 |
| `poolUsd` | The new Shift's pool | the Team plan's `pool` |
| `note` | Operator text added to every Run's briefing in the new Shift | none |
| `commandId` | Idempotency key. A replay returns the first result | required when a body is sent |
| `expectedState` | The state the caller saw. A mismatch returns 409 | none |

With no body the route does what VIK-606 specifies: restart from Round 1.

### What Ploeg does

1. **Authorize.** The consumer needs `execute` for the item's Team, and the
   acting user is recorded as for Cancel. `poolUsd` may not exceed the
   consumer's `maxBudgetUsd`. Operator-owned items return 409, as Cancel does.
2. **Check the state.** Allowed: `needs_human`, `stale` and `awaiting_review`.
   A live item (`ingested`, `queued`, `leased`, or any open Shift) returns 409.
   `done` (merged) and `withdrawn` (deliberately stopped) return 409. The
   tracker's re-assignment still restarts those, unchanged.
3. **Check the starting Round.** `fromRound` must name a Round in the Team's
   current plan. If the Rounds before `fromRound` include a writing Round, an
   earlier Shift of this item must have recorded a writer's `pr_opened` or
   `pr_updated`. Otherwise the route returns 422 `nothing_to_review`. A reviewer
   run over a branch that was never written is the failure the 2026-09-29
   incident report describes (factor 5).
4. **Requeue.** The item goes to `queued` with `attempts` and `infra_failures`
   reset, the same transition `IngestAssigned` makes, extracted into one store
   function that both call.
5. **Open a new Shift on the same branch that starts at `fromRound`.** Earlier
   Rounds are not run. The earlier Shift stays closed with its close reason.
   Per-Round attempt counts are derived per Shift, so the new Shift starts with
   full budgets. Fix rounds (ADR-0017) are counted against the new Shift's
   position in the plan, exactly as for any Shift.
6. **Brief the Runs.** Every Run of the new Shift is briefed with the previous
   Shift's close reason, the failed Runs' `failure_reason` and summaries, and
   the operator's `note`. The pull request remains the blackboard (ADR-0011),
   so earlier findings stay there too.
7. **Record and tell.** Audit `work_item.requeued` with the actor,
   `fromRound`, the pool, the previous Shift id, the `commandId` and the note.
   Comment on the tracker task and the pull request: "Restarted from Round N by
   <actor>".

### What Vloer offers

Vloer offers only the buttons the item's state allows, and every button calls
this route. The confirm dialog shows the Rounds that will run and the pool
that will be authorized.

| Button | Offered when | `fromRound` |
| --- | --- | --- |
| Run the review again | Shift closed `review_failed` (ADR-0043), pull request open | the failed reading Round |
| Retry from this Round | Closed `writing_run_failed_repeatedly`, `writing_run_killed_repeatedly`, a `stuck` Run, or a pool that could not fund the next Round | that Round |
| Start again | `needs_human` or `stale` | 1 |
| Retry with a note | any of the above | as the chosen button, plus `note` |

Adding budget is not a separate action: "Retry from this Round" with a larger
`poolUsd` is the same call. Cancel stays for live items only, and Vloer hides it
for everything else.

### Authority

No agent path reaches this route. It needs an operator consumer with
`execute`, and ploegd issues workers no operator credential. When ADR-0036's
escalation briefs land, accepting a `requeue_with_amendment` brief calls this
route as the owner's act, with the brief's amendment as `note`. A future
`ploegctl requeue` calls the same route.

### Consequences

* Good, because the owner restarts stopped work from where they are looking at
  it, and pays only for the Rounds that need re-running. Re-running Shift 118's
  review authorizes the reviewer's US$ 0,40 cap, not US$ 2,40 with the builder.
* Good, because the closed Shift is never rewritten. The audit trail reads
  "Shift 118 closed `review_failed`; Shift 119 opened by the owner at Round 2".
* Good, because VIK-606, ADR-0036 and `ploegctl` get one route with one set of
  checks instead of three.
* Good, because it needs no new state and no new authority. It is re-assignment
  without leaving Vloer.
* Bad, because a Shift can now start part-way through its plan, and code that
  assumed every Shift starts at Round 1 must be found. `fixRoundsRun` and
  `readyForReview` read the plan position and are covered by the tests below.
* Bad, because resetting `attempts` on every restart means a person can keep
  retrying a hopeless item. Accepted: each restart is one human action with a
  confirmed pool, and the tracker has always allowed the same.
* Bad, because the plan is read at restart time, not from the closed Shift. A
  Team whose plan changed in between restarts into the new plan. Accepted, and
  the route rejects a `fromRound` the new plan does not have.

### Confirmation

In `.forgejo/workflows/on_pull_request.yml`, `go test ./...` in `apps/ploeg`
covers:

* `pkg/store`: requeueing from `needs_human`, `stale` and `awaiting_review`
  resets both counters. Every other state is refused without a change. The
  extracted transition is the one `IngestAssigned` calls, and the existing
  ingest tests pass unmodified.
* `pkg/shiftengine`: a Shift opened at `fromRound = 2` materialises only
  Round 2's Roles, on the item's existing branch. Its fix rounds count from its
  own position.
* `pkg/httpapi`: 403 without `execute`; 409 for a live, `done`, `withdrawn` or
  operator-owned item; 422 `nothing_to_review` when no writer reported a pull
  request; `poolUsd` above `maxBudgetUsd` refused. A repeated `commandId`
  returns the first result and opens one Shift. One `work_item.requeued` audit
  row names the actor.

`npm test` in `apps/vloer` covers which buttons each state shows and the body
each button sends. `go test ./internal/ledger/` gates this record.

## Pros and Cons of the Options

### Reopen the closed Shift at the failed Round

* Good, because the whole attempt stays in one Shift.
* Bad, because it rewrites a closed record. `closed_at` and `close_reason`
  would no longer mean what they meant when the tracker was told.
* Bad, because the Shift's pool and attempt counts carry over, so a restart
  after a budget park or an attempt cap stops again at once, unless the counts
  are edited, which ADR-0012's discipline forbids.

### A separate endpoint per action

* Good, because each endpoint is simple to read.
* Bad, because every endpoint repeats the same authorization, state, pool and
  idempotency checks, and ADR-0036 would need yet another.
* Bad, because the actions differ only in their starting Round and pool.

### A Follow-Up Work Item per restart

* Good, because `CreateRepairFollowUp` already opens a child Work Item on an
  existing branch.
* Bad, because every restart adds a Work Item to the board and the lanes. Its
  history splits across two items, and the tracker task maps to the wrong one.
* Bad, because Follow-Ups are how Ploeg reacts to forge events on its own. A
  person's restart is a different act and should read as one.

### Keep the tracker as the only restart

* Good, because it needs nothing built.
* Bad, because it can only start from Round 1, and it needs the owner to leave
  Vloer and know the assign trick. This is the cost VIK-606 already records.

## Re-evaluation triggers

* ADR-0036's escalation lead ships. Check that accepting a brief needs nothing
  this route lacks.
* Restarts from the same close reason exceed five in a month. The automatic
  path for that reason is missing or wrong, and fixing it beats a button.
* A Round gains an explicit id (the plan index stops being positional).
  `fromRound` should then name the Round, not count it.
* A Team runs a synthesized (plan-less) Shift in production. Its one Round
  makes `fromRound` meaningless, and the route should say so.

## More Information

* Technical story: VIK-606 (`unfold: re-queue a needs_human Work Item from
  Vloer`) is the first slice. Starting at a chosen Round and the Vloer buttons
  are tracked beside it.
* 2026-10-01 — accepted by the owner, with the options as written: restart
  `needs_human`, `stale` and `awaiting_review` only; a restart resets the
  attempt counts and authorizes a pool the operator confirms. Build order:
  after ADR-0043's VIK-1304, then VIK-606, VIK-1596, VIK-1597.
* Evidence: `docs/research/2026-09-29-incident-work-item-138.md`; Shift 118 /
  unfold PR #45 (reviewer Run 209, "acp agent stopped responding").
* [ADR-0019](0019-a-failed-writing-run-reopens-its-round.md): why a Shift's
  round is its plan index, and why restarts must not skip a planned Round.
* [ADR-0043](0043-a-failed-reading-run-is-retried-and-a-missing-review-closes-review-failed.md):
  the automatic retry that runs before a person needs this route.
* [ADR-0036](0036-stuck-work-reaches-the-owner-as-a-cited-proposal-not-an-agent-decision.md):
  `requeue_with_amendment` becomes a call to this route with `note`.
* [ADR-0024](0024-operator-work-uses-one-execution-authority.md) and
  [ADR-0025](0025-management-authority-stays-in-the-control-plane.md): Vloer
  asks and Ploeg decides.
