---
status: proposed
date: 2026-08-08
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A failed writing Run re-opens its Round; a failed reading Run does not

## Context and Problem Statement

`shiftengine.evaluate` freezes the plan when a Run in a completed Round reported
`stuck`, and has no case for `failed`. A Run the sweeper reclaimed is, as far as
`RoundComplete` is concerned, simply finished — so the Round completes and the
next Round opens.

The shift-orchestration spec appears to sanction this: *"a swept Run does not
block its Round forever … the Round can complete and the Shift advances."* But
that requirement was reasoned about **readers**, where it is right: a reader
that dies costs an opinion, and stalling an item over a missing opinion would
be worse. Applied to the **writer** it means the branch was never written and
every later Round reasons about work that does not exist.

Observed in the bench's crash drill: the writing Round's only Run was reclaimed
as `failed`/`lease_lost`, the engine advanced, the reviewer reviewed a branch
that had never been written, returned `approve`, and the Shift closed
**`review_approved` with no pull request** — while the tracker comment
correctly said Ploeg had stopped without opening one. The two disagreed, and
the reassuring one is the field an operator greps.

The question this record answers: what happens to the plan when a Run fails,
and does the answer depend on whether that Run could write.

## Decision Drivers

* R2 — crash-safety is mechanical. A node eviction should not need a person.
* R4 — `stuck` says a human is needed and no retry fixes that. `failed` is the
  sweeper's verdict on a pod that stopped renewing, so it is retryable by
  construction. The two must not be conflated.
* A close reason is what an operator greps six weeks later. `review_approved`
  for a Shift that produced nothing is worse than any honest failure.
* ADR-0012's discipline: derive counts from what happened; a counter can
  disagree with reality.

## Considered Options

* **Re-open the failed writer's Round in place, capped; leave readers alone**
* Park the Shift at `needs_human` whenever a writing Run fails
* Requeue the whole Work Item and start a fresh Shift
* Open a NEW Round containing the writer, as `OpenRound` already does

## Decision Outcome

Chosen option: "**re-open the failed writer's Round in place, capped; leave
readers alone**".

When a completed Round's writing Role has no Outcome other than `failed`, the
engine re-opens that Role **in the round the Shift is already on** —
`store.ReopenRound` inserts a pending Run at the current round number without
incrementing it. After `store.MaxRunAttempts` attempts in that Round the Shift
closes at `needs_human` with close reason `writing_run_failed_repeatedly`.

Three details carry the decision:

1. **In place, not a new Round.** `shifts.round` doubles as the index into the
   Team's plan (`tp.Rounds[si.Round]`), so opening a fresh Round to retry would
   silently skip the next planned one. Re-opening in place keeps the plan index
   true and keeps rounds contiguous.
2. **The attempt count is derived** from the `agent_runs` rows in that
   `(shift, round, role)`, never stored — the same rule `reserved` follows.
3. **Readers are untouched.** The spec's swept-Run scenario stands, narrowed to
   what it was always about.

This applies to **planned** Shifts only. Under uniform dispatch a synthesized
one-writer Shift already does the right thing: the plan exhausts, `close`
settles by the run's own Outcome, and `failed` maps to `queued` — the
attempt-capped requeue R5 requires. There is no later Round there to step over.

### Consequences

* Good, because the factory self-heals from the failure it is most likely to
  meet — a pod evicted under memory pressure — without a person, which is what
  R2 promises.
* Good, because a Shift can no longer report an approval it did not earn. The
  close reason and the tracker comment now agree.
* Good, because it needs no schema change and no new counter: one store method
  and one branch in the advancement rule.
* Bad, because a Shift's cost is now less predictable from its plan: a retried
  writer spends from the pool again. Accepted — the pool is the real ceiling
  and is enforced before every Run (ADR-0012).
* Bad, because `MaxRunAttempts` is a second retry bound alongside
  `MaxAttempts`, and the two now govern different scopes (a Role within a
  Round; a Work Item across Shifts). Accepted deliberately: `work_items.attempts`
  increments per role claim, so it never meant "attempts at this work" once
  Shifts landed, and reusing it here would have made a three-role plan exhaust
  its budget in one clean pass.
* Bad, because a writer that fails for a NON-transient reason now burns three
  runs before parking. Mitigated by the cap being small, and by the failure
  being visible in `agent_runs` the whole time.

### Confirmation

`go test ./pkg/shiftengine/` — `TestFailedWriter_ReopensItsOwnRound` (the Round
re-opens, the counter does not advance, and no reviewer Run is opened over the
unwritten branch), `TestFailedWriter_ParksTheShiftAtTheAttemptCap` (the cap
closes the Shift at `needs_human` with this record's close reason), and
`TestFailedReader_StillAdvancesTheRound` (the spec's reader scenario, intact).
The first two fail against the unfixed engine.

End to end, `ploeg-bench`'s `hang` scenario SIGKILLs the worker mid-run so the
lease genuinely lapses, and asserts **L1-16** (a Shift that closes approved
produced a pull request) and **L1-17** (a failed writing Run is retried, or the
Shift stops). Both are red against the unfixed code.

## Pros and Cons of the Options

### Park the Shift at `needs_human` whenever a writing Run fails

* Good, because it is the smallest possible change and cannot loop.
* Bad, because it turns every transient pod eviction into human work, which is
  the opposite of what R2 exists for — and the dark factory's whole premise is
  that nobody is watching.

### Requeue the whole Work Item and start a fresh Shift

* Good, because it reuses the pre-Shift path exactly and needs no new concept.
* Bad, because it throws away the readers' findings and pays for them again.
* Bad, because `work_items.attempts` increments per role claim, so the cap that
  would bound it is already the wrong shape.

### Open a NEW Round containing the writer

* Good, because `OpenRound` exists and needs no new store method.
* Bad, because `shifts.round` is the plan index: the retry would consume the
  slot belonging to the next planned Round, silently skipping it. The bug would
  be a reviewer that never runs, which is harder to see than the one this
  record fixes.

## Re-evaluation triggers

* A writing Run reaches `MaxRunAttempts` in production more than once in a
  month — the failure is not transient and retrying is the wrong response.
* The plan index stops being positional (a Round gains an explicit id), which
  removes the reason retries must happen in place.
* A Round is ever allowed more than one writer, which would make "the Round's
  writing Role" ambiguous.
* Backlog #109 lands (settling spend for swept runs): a retried writer's
  earlier attempt would then also draw on the pool, and the interaction with
  the cap should be re-read.

## More Information

* 2026-08-13 — the first re-evaluation trigger fired: work item 98 reached
  `MaxRunAttempts` in production, having also reached it on 08-11 and 08-12.
* 2026-08-26 — refined by
  [ADR-0021](0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md):
  the cap above is now the **agent's** budget, and infrastructure-caused
  attempts are counted against `store.MaxInfraFailures` separately. Everything
  else this record decided stands.
* 2026-10-01 — refined by
  [ADR-0043](0043-a-failed-reading-run-is-retried-and-a-missing-review-closes-review-failed.md)
  (proposed): a failed reading Run is now retried in place under the same
  budgets, and a review that never came closes `review_failed`. "Readers are
  untouched" above no longer holds once that record is implemented.
* Evidence: `docs/research/2026-08-08-benchmarking-the-loop.md`, and the
  `hang` scenario in `webgrip/ploeg-bench`.
* [ADR-0010](0010-shift-owns-the-item-lease-owns-the-branch.md) — a Round is
  readers or one writer, which is what makes "the Round's writer" well defined.
* [ADR-0012](0012-two-level-budgets-authorized-and-settled.md) — the pool that
  bounds a retry's spend, and the derive-never-count discipline.
* [ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md) — the other
  bounded loop; this one is its crash-side twin.
* `openspec/specs/shift-orchestration/spec.md` — the swept-Run requirement this
  narrows, and the new writer requirement.
