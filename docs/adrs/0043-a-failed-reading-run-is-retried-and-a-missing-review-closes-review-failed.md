---
status: accepted
date: 2026-10-01
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# A failed reading Run is retried in its Round, and a review that never came closes `review_failed`

## Context and Problem Statement

[ADR-0019](0019-a-failed-writing-run-reopens-its-round.md) re-opens a failed
writing Run in place and leaves readers alone: "a reader that dies costs an
opinion, and stalling an item over a missing opinion would be worse". The code
does exactly that. `retryFailedWriter` logs "a reading Run failed; its findings
are missing from this Round" and returns (`pkg/shiftengine/failedwriter.go`).

Production shows what the missing opinion costs. On 2026-09-28 reviewer Run 144
of Shift 90 was OOMKilled. On 2026-09-29 the ACP idle watchdog stopped reviewer
Run 209 of Shift 118 ("acp: no protocol activity idleTimeout=10m0s events=29").
Both Shifts closed `plan_exhausted`. Because a writer had opened a pull request,
`readyForReview` (`pkg/shiftengine/engine.go`) settled both items
`awaiting_review`, and the tracker comment said "plan complete". Vloer showed
PR #45 as "Ready for your review" with "No agent verdict". Nothing in Ploeg
would run that review again. A retry would have cost at most the reviewer's
US$ 0,40 cap. The only way forward was to re-assign the tracker task, which
also pays for the builder again.

ADR-0019 was right that a missing opinion must not stall an item. It did not
weigh a cheap retry, and it let the close reason hide the missing review.

The question this record answers: what happens to a Round, and to the Shift's
close, when a reading Run fails.

## Decision Drivers

* R2: crash-safety is mechanical. An evicted or wedged reviewer should not need
  a person, any more than an evicted writer does.
* A close reason is what an operator greps six weeks later.
  `plan_exhausted` for a Shift whose review never happened says the opposite of
  what happened.
* A reader never writes, so retrying one cannot damage the branch. It is also
  the cheapest Run in a plan (bronze: US$ 0,40 cap against US$ 2,00 for the
  builder).
* ADR-0012's discipline: derive attempt counts from `agent_runs` rows, never
  from a counter.
* Keep one retry rule where one is enough. Two rules for "a Run in this Round
  failed" would drift apart.

## Considered Options

* **Retry a failed reader in place under the writer's two budgets; when they
  run out, close `review_failed`**
* Retry a failed reader exactly once, then advance as today
* Keep ADR-0019 as is and fix only the close reason
* Freeze the plan at `needs_human` when a reader fails

## Decision Outcome

Chosen option: "retry a failed reader in place under the writer's two budgets;
when they run out, close `review_failed`", because it removes the
review-that-never-happened close without a new retry concept. The machinery
ADR-0019 and ADR-0021 built for writers applies to readers unchanged.

1. **Retry in place.** When a completed Round has a reading Role whose Runs
   all ended `failed` (`store.FailedRunsInRound` already returns it, with
   `Writes` false), the engine re-opens that Role with `store.ReopenRound` in
   the Round the Shift is on. Readers in the same Round that succeeded are not
   re-run. The round counter does not advance.
2. **Same budgets, same split.** The Role's `AgentAttempts()` are bounded by
   `store.MaxRunAttempts` and its `InfraAttempts` by `store.MaxInfraFailures`,
   exactly as ADR-0021 bounds the writer. The pool is checked before the Run is
   spawned (ADR-0012), and an unfundable retry is handled as today: the Shift
   parks at `needs_human` naming the spend.
3. **A writer's failure still comes first.** If the Round's writer failed, the
   ADR-0019 path runs and readers are not considered. A Round is readers or
   one writer (ADR-0010), so this only matters for clarity.
4. **When the budgets run out, the Round completes and the plan advances**, as
   ADR-0019 decided for readers. The Shift remembers why: if the last reading
   Round after the last writing Round has a Role with no non-failed Outcome,
   the Shift closes with the new reason **`review_failed`** instead of
   `plan_exhausted`.
5. **`review_failed` with a writer's pull request still settles
   `awaiting_review`.** The pull request is real and a person can review it.
   The tracker comment and Vloer both say that no agent reviewed it and name
   the reviewer's `failure_reason`. They never say "plan complete".
6. **`stuck` is unchanged.** A reader that reports `stuck` still freezes the
   plan (R4). Only `failed` is retried.

What this decision does not change: an ACP watchdog kill stays `failed` with
`failure_reason = agent_error`. Charging it to the infrastructure budget
instead was considered and rejected. The wedged process is the harness
configuration's fault, not the node's. The infrastructure budget
(`MaxInfraFailures`, 10) has no backoff, so a broken harness would spend ten
Runs before stopping instead of three. The fix for Runs that were busy, not
wedged, is to count model traffic as activity (VIK-1291), not to reclassify
the kill.

### Consequences

* Good, because the common reviewer failure (an evicted pod, a wedged harness,
  an OOMKill) now heals itself, and a reviewer that fails is retried at reviewer
  cost, not builder cost.
* Good, because `review_failed` makes the close reason, the tracker comment and
  Vloer agree about what happened, and it is greppable.
* Good, because it needs no schema change. The query already partitions readers
  from writers, and the new close reason is a string.
* Bad, because a Shift can now spend up to `MaxRunAttempts` reader Runs where it
  spent one. Accepted: each is capped by the Role's `cap`, and the pool check
  runs before every Run.
* Bad, because a reviewer that fails for a non-transient reason (a broken
  harness profile) now takes up to three Runs to reach `review_failed`.
  Mitigated by the same small cap that bounds writers, and by the failures
  being visible in `agent_runs` the whole time.
* Bad, because the openspec scenario "A swept READING Run does not block its
  Round forever" must be reworded. Its intent (never stall) survives, and its
  wording (advance at once) does not.

### Confirmation

`go test ./pkg/shiftengine/` in `.forgejo/workflows/on_pull_request.yml`:

* `TestFailedReader_ReopensItsOwnRound` (a failed reader re-opens in place and
  the counter does not advance) replaces `TestFailedReader_StillAdvancesTheRound`,
  which pins the old behaviour.
* A reader failing at `MaxRunAttempts`, or at `MaxInfraFailures` infra
  attempts, lets the Round complete and closes the Shift `review_failed`.
* Replaying Shift 118 (writer `pr_opened`, reader `failed` three times) settles
  `awaiting_review` with close reason `review_failed`. That test fails on
  `6de34d0`.
* A reader that ran and gave no verdict still closes `plan_exhausted`.

`go test ./pkg/shiftengine/` also pins the tracker wording for `review_failed`
(`closeMessage`, `trackerMessage`). After deployment, the next failed reviewer
shows `close_reason = review_failed` on its Shift. `go test ./internal/ledger/`
gates this record.

## Pros and Cons of the Options

### Retry a failed reader exactly once, then advance as today

* Good, because it bounds a reader's retries at one, below the writer's three.
* Bad, because it adds a third attempt bound next to `MaxRunAttempts` and
  `MaxInfraFailures`, for the same question the other two already answer.
* Bad, because one retry does not survive a cluster that evicts twice in a
  row, and ADR-0021 exists because evictions arrive in threes.

### Keep ADR-0019 as is and fix only the close reason

* Good, because it is the smallest change and cannot add spend.
* Bad, because every transient reviewer failure still becomes human work,
  which is what R2 exists to prevent.

### Freeze the plan at `needs_human` when a reader fails

* Good, because nothing reads as reviewed when it was not.
* Bad, because it stalls an item over a missing opinion, the failure ADR-0019
  correctly refused. It also hides a real, reviewable pull request behind a
  `needs_human` label.

## Re-evaluation triggers

* A reading Role reaches `MaxRunAttempts` in production more than once in a
  month. The failure is not transient for readers, and three attempts may be
  too many.
* VIK-1291 lands (model traffic counts as activity). If ACP kills then stop,
  the retry budget is mostly spent on evictions, and the split is worth
  re-reading.
* A Round is ever allowed to mix readers and a writer, which would make "a
  reading Role's Runs all failed" ambiguous.
* An operator restart from a chosen Round
  ([ADR-0044](0044-an-operator-restarts-stopped-work-from-a-round-they-choose.md))
  is used on `review_failed` Shifts more than twice a month. The automatic
  retry is not doing its job.

## More Information

* Technical story: VIK-1304 (`ploeg: close a Shift whose reviewer Run failed as
  review_failed`), which this record decides.
* 2026-10-01 — accepted by the owner, choosing the writer's budgets over a
  single retry or no retry. Build order: this record's VIK-1304 goes first.
* Evidence: `docs/research/2026-09-29-incident-work-item-138.md` (Work Item
  138, Shifts 113 and 118); Shift 90 / unfold PR #7 (OOMKilled reviewer).
* Refines [ADR-0019](0019-a-failed-writing-run-reopens-its-round.md) (readers
  are no longer left alone) and applies
  [ADR-0021](0021-infra-failures-and-agent-failures-get-separate-retry-budgets.md)'s
  split to readers.
* [ADR-0017](0017-the-review-loop-is-verdict-driven-and-capped.md): a retried
  reader's verdict drives the fix-round loop like any other.
* [ADR-0044](0044-an-operator-restarts-stopped-work-from-a-round-they-choose.md):
  the operator's way back in when this retry runs out.
* `openspec/specs/shift-orchestration/spec.md`: the swept-READING-Run scenario
  to reword in the implementing change.
